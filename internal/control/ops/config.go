package ops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/config"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/jsonx"
)

// ConfigPublisher is the T05 publish surface. *config.Publisher implements
// it. The interface keeps ops tests free of Redis while production still runs
// the real version protocol (t_config_item -> t_config_publish -> snapshot).
type ConfigPublisher interface {
	Publish(ctx context.Context, changes []config.Change, operatorID uint64) (*config.PublishResult, error)
}

type configItemView struct {
	Key             string           `json:"key"`
	Namespace       string           `json:"namespace"`
	Value           jsonx.RawMessage `json:"value"`
	RuntimeWritable bool             `json:"runtime_writable"`
	Version         int64            `json:"version"`
	Source          string           `json:"source"` // ROW / DEFAULT
}

type configListResponse struct {
	Items []configItemView `json:"items"`
	Total int              `json:"total"`
}

type configUpdateRequest struct {
	Values map[string]jsonx.RawMessage `json:"values"`
}

type configUpdateResponse struct {
	Version     int64    `json:"version"`
	PublishID   string   `json:"publish_id"`
	Namespaces  []string `json:"namespaces"`
	ChangedKeys []string `json:"changed_keys"`
}

// GetConfig implements GET /api/admin/config. It lists all 45 keys, flags the
// 12 runtime-writable ones, and falls back to the registry default when a row
// is absent. It exposes no secret: the 45 frozen keys contain none by design.
func (s *Service) GetConfig(w http.ResponseWriter, r *http.Request) {
	type row struct {
		value   jsonx.RawMessage
		version int64
	}
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT cfg_key, cfg_value, version FROM t_config_item
		 WHERE scope = 'GLOBAL' AND scope_id = 0 AND enabled = 1`)
	if err != nil {
		s.logError(r.Context(), "ops_config_list_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	defer rows.Close()

	stored := make(map[string]row, config.KeyCount)
	for rows.Next() {
		var (
			key     string
			value   jsonx.RawMessage
			version int64
		)
		if err := rows.Scan(&key, &value, &version); err != nil {
			s.logError(r.Context(), "ops_config_scan_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		stored[key] = row{value: value, version: version}
	}
	if err := rows.Err(); err != nil {
		s.logError(r.Context(), "ops_config_rows_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	keys := config.AllKeys()
	items := make([]configItemView, 0, len(keys))
	for _, key := range keys {
		meta, ok := config.Meta(key)
		if !ok {
			continue
		}
		item := configItemView{
			Key:             string(key),
			Namespace:       string(meta.Namespace),
			RuntimeWritable: meta.RuntimeWritable,
			Source:          "DEFAULT",
			Value:           jsonx.RawMessage(meta.Default),
		}
		if got, ok := stored[string(key)]; ok {
			item.Value = got.value
			item.Version = got.version
			item.Source = "ROW"
		}
		items = append(items, item)
	}
	_ = mw.WriteJSON(w, http.StatusOK, configListResponse{Items: items, Total: len(items)})
}

// PutConfig implements PUT /api/admin/config.
//
// Only the 12 runtime-writable keys are accepted. There is deliberately no
// "clear all caches" endpoint (hard constraint 16): the publisher invalidates
// exactly the namespaces touched by this request.
func (s *Service) PutConfig(w http.ResponseWriter, r *http.Request) {
	var req configUpdateRequest
	if apiErr := decodeJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if len(req.Values) == 0 {
		mw.WriteError(w, r, mw.InvalidField("values"))
		return
	}
	if len(req.Values) > config.RuntimeKeyCount {
		mw.WriteError(w, r, apierr.Newf(apierr.CodeConfigValueInvalid,
			fmt.Sprintf("一次最多写入 %d 个运行时键，当前 %d 个", config.RuntimeKeyCount, len(req.Values))))
		return
	}

	keys := make([]string, 0, len(req.Values))
	for key := range req.Values {
		keys = append(keys, key)
	}
	sortStrings(keys)

	changes := make([]config.Change, 0, len(keys))
	seen := make(map[config.Key]bool, len(keys))
	for _, rawKey := range keys {
		key := config.Key(strings.TrimSpace(rawKey))
		if seen[key] {
			mw.WriteError(w, r, apierr.Newf(apierr.CodeConfigValueInvalid,
				fmt.Sprintf("%s 在请求中重复出现", key)))
			return
		}
		seen[key] = true
		meta, ok := config.Meta(key)
		if !ok {
			mw.WriteError(w, r, apierr.Newf(apierr.CodeConfigValueInvalid,
				fmt.Sprintf("%s 不是已登记的配置键", key)))
			return
		}
		if !meta.RuntimeWritable {
			reason := "不在 12 个运行时白名单内"
			if meta.Namespace == config.NsUnassigned {
				reason = "不属于任何命名空间，不可运行时写入"
			}
			mw.WriteError(w, r, apierr.Newf(apierr.CodeConfigValueInvalid,
				fmt.Sprintf("%s %s", key, reason)))
			return
		}
		value := req.Values[rawKey]
		if err := config.ValidateRuntime(key, value); err != nil {
			// Use the reason, not Error(), which prefixes the key name:
			// apierr's secret redactor treats a literal "key_" + 8 chars as a
			// credential (quota.key_daily_mode matches) and would replace it
			// with [redacted-key] in the response.
			var ve *config.ValidationError
			msg := err.Error()
			if errors.As(err, &ve) {
				msg = ve.Reason
			}
			mw.WriteError(w, r, apierr.Newf(apierr.CodeConfigValueInvalid, msg))
			return
		}
		changes = append(changes, config.Change{Key: key, Raw: value})
	}

	if s.ConfigPublisher == nil {
		mw.WriteError(w, r, apierr.New(apierr.CodeConfigUnavailable))
		return
	}
	uid, _, apiErr := actor(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	before, err := loadConfigMap(r.Context(), s.DB, changes)
	if err != nil {
		s.logError(r.Context(), "ops_config_before_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	result, err := s.ConfigPublisher.Publish(r.Context(), changes, uint64(uid))
	if err != nil {
		s.logError(r.Context(), "ops_config_publish_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeConfigUnavailable).WithCause(err))
		return
	}
	after := make(map[string]jsonx.RawMessage, len(changes))
	for _, ch := range changes {
		after[string(ch.Key)] = ch.Raw
	}
	if err := s.auditConfigWrite(r, before, after); err != nil {
		s.logError(r.Context(), "ops_config_audit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	namespaces := make([]string, 0, len(result.Namespaces))
	for _, ns := range result.Namespaces {
		namespaces = append(namespaces, string(ns))
	}
	changed := make([]string, 0, len(result.ChangedKeys))
	for _, key := range result.ChangedKeys {
		changed = append(changed, string(key))
	}
	_ = mw.WriteJSON(w, http.StatusOK, configUpdateResponse{
		Version: result.Version, PublishID: result.PublishID,
		Namespaces: namespaces, ChangedKeys: changed,
	})
}

// auditConfigWrite runs after the publish because the publish owns its own
// transaction. A failure is surfaced: an unlogged config change is a P0.
func (s *Service) auditConfigWrite(r *http.Request, before, after map[string]jsonx.RawMessage) error {
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	if err := s.insertAudit(r.Context(), tx, r, "CONFIG", "UPDATE", "t_config_item", 0,
		before, after, ""); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func loadConfigMap(ctx context.Context, q priceDB, changes []config.Change) (map[string]jsonx.RawMessage, error) {
	out := make(map[string]jsonx.RawMessage, len(changes))
	for _, ch := range changes {
		var raw jsonx.RawMessage
		err := q.QueryRowContext(ctx, `
			SELECT cfg_value FROM t_config_item
			 WHERE scope = 'GLOBAL' AND scope_id = 0 AND enabled = 1 AND cfg_key = ?`,
			string(ch.Key)).Scan(&raw)
		if err == nil {
			out[string(ch.Key)] = raw
			continue
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if meta, ok := config.Meta(ch.Key); ok {
			out[string(ch.Key)] = jsonx.RawMessage(meta.Default)
		}
	}
	return out, nil
}

func sortStrings(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
