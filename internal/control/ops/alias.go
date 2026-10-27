package ops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
)

// modelQueryer is satisfied by *sql.DB and *sql.Tx. The alias checks run
// inside the caller's transaction so a conflict and its refusal commit or
// roll back together.
type modelQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// modelRef names the model a conflicting name or alias belongs to. The id and
// public name both travel in the refusal message (F22-2: "列出冲突模型").
type modelRef struct {
	ID        int64
	ModelName string
}

type aliasView struct {
	AliasName string    `json:"alias_name"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
}

type aliasListResponse struct {
	ModelID int64       `json:"model_id"`
	Aliases []aliasView `json:"aliases"`
}

type aliasCreateRequest struct {
	AliasName *string `json:"alias_name"`
}

type aliasDetailResponse struct {
	Alias aliasView `json:"alias"`
}

// ListAliases implements GET /api/admin/models/{id}/aliases.
func (s *Service) ListAliases(w http.ResponseWriter, r *http.Request) {
	id, apiErr := pathID(r, "id")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	ctx := r.Context()
	ok, err := modelExists(ctx, s.DB, id)
	if err != nil {
		s.logError(ctx, "ops_alias_model_read_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if !ok {
		mw.WriteError(w, r, apierr.New(apierr.CodeResourceNotFound))
		return
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT alias_name, source, created_at FROM t_model_alias
		 WHERE model_id = ? AND enabled = 1
		 ORDER BY alias_name`, id)
	if err != nil {
		s.logError(ctx, "ops_alias_list_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	defer rows.Close()
	items := make([]aliasView, 0, 8)
	for rows.Next() {
		var v aliasView
		if err := rows.Scan(&v.AliasName, &v.Source, &v.CreatedAt); err != nil {
			s.logError(ctx, "ops_alias_scan_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		v.CreatedAt = v.CreatedAt.UTC()
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		s.logError(ctx, "ops_alias_rows_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, aliasListResponse{ModelID: id, Aliases: items})
}

// CreateAlias implements POST /api/admin/models/{id}/aliases.
func (s *Service) CreateAlias(w http.ResponseWriter, r *http.Request) {
	id, apiErr := pathID(r, "id")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	var req aliasCreateRequest
	if apiErr := decodeJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if req.AliasName == nil {
		mw.WriteError(w, r, mw.InvalidField("alias_name"))
		return
	}
	alias := strings.TrimSpace(*req.AliasName)
	if alias == "" || len([]rune(alias)) > 128 {
		mw.WriteError(w, r, mw.InvalidField("alias_name"))
		return
	}

	ctx := r.Context()
	now := s.now()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		s.logError(ctx, "ops_alias_begin_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	ok, err := modelExists(ctx, tx, id)
	if err != nil {
		s.logError(ctx, "ops_alias_model_read_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if !ok {
		mw.WriteError(w, r, apierr.New(apierr.CodeResourceNotFound))
		return
	}
	if ref, found, apiErr := aliasOwner(ctx, tx, alias, id); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	} else if found {
		mw.WriteError(w, r, aliasTaken(alias, ref))
		return
	}
	if ref, found, apiErr := modelNameOwner(ctx, tx, alias, id); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	} else if found {
		mw.WriteError(w, r, aliasClashesWithName(alias, ref))
		return
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO t_model_alias (alias_name, model_id, source, enabled, created_at)
		VALUES (?,?, 'OPS', 1, ?)`, alias, id, now); err != nil {
		// uk_alias_name is the final arbiter. Translate 1062 into the same
		// readable conflict the pre-check produces; never echo the MySQL text.
		if isDuplicateKey(err) {
			if ref, found, apiErr := aliasOwner(ctx, tx, alias, id); apiErr == nil && found {
				mw.WriteError(w, r, aliasTaken(alias, ref))
				return
			}
			mw.WriteError(w, r, apierr.Newf(apierr.CodeResourceStateConflict,
				fmt.Sprintf("别名 %s 已被其他模型占用", alias)))
			return
		}
		s.logError(ctx, "ops_alias_insert_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if err := s.insertAudit(ctx, tx, r, "MODEL", "MODEL_ALIAS_CREATE", "t_model", id, nil,
		map[string]any{"alias_name": alias}, ""); err != nil {
		s.logError(ctx, "ops_alias_audit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if err := tx.Commit(); err != nil {
		s.logError(ctx, "ops_alias_commit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed = true
	_ = mw.WriteJSON(w, http.StatusCreated, aliasDetailResponse{
		Alias: aliasView{AliasName: alias, Source: "OPS", CreatedAt: now.UTC()},
	})
}

// DeleteAlias implements DELETE /api/admin/models/{id}/aliases/{alias}.
func (s *Service) DeleteAlias(w http.ResponseWriter, r *http.Request) {
	id, apiErr := pathID(r, "id")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	alias := strings.TrimSpace(r.PathValue("alias"))
	if alias == "" || len([]rune(alias)) > 128 {
		mw.WriteError(w, r, apierr.New(apierr.CodeResourceNotFound))
		return
	}

	ctx := r.Context()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		s.logError(ctx, "ops_alias_delete_begin_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	ok, err := modelExists(ctx, tx, id)
	if err != nil {
		s.logError(ctx, "ops_alias_model_read_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if !ok {
		mw.WriteError(w, r, apierr.New(apierr.CodeResourceNotFound))
		return
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM t_model_alias WHERE model_id = ? AND alias_name = ?`, id, alias)
	if err != nil {
		s.logError(ctx, "ops_alias_delete_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		mw.WriteError(w, r, apierr.New(apierr.CodeResourceNotFound))
		return
	}
	if err := s.insertAudit(ctx, tx, r, "MODEL", "MODEL_ALIAS_DELETE", "t_model", id,
		map[string]any{"alias_name": alias}, nil, ""); err != nil {
		s.logError(ctx, "ops_alias_audit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if err := tx.Commit(); err != nil {
		s.logError(ctx, "ops_alias_commit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed = true
	_ = mw.WriteJSON(w, http.StatusOK, map[string]any{"deleted": true, "alias_name": alias})
}

func modelExists(ctx context.Context, q modelQueryer, id int64) (bool, error) {
	var found int64
	err := q.QueryRowContext(ctx,
		`SELECT id FROM t_model WHERE id = ? AND deleted_at IS NULL`, id).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// aliasOwner finds the model that already owns alias. A soft-deleted model
// still owns its row: uk_alias_name has no deleted_at predicate, so allowing
// reuse here would pass the pre-check and fail the INSERT.
func aliasOwner(ctx context.Context, q modelQueryer, alias string, excludeModelID int64) (modelRef, bool, *apierr.APIError) {
	var ref modelRef
	err := q.QueryRowContext(ctx, `
		SELECT a.model_id, m.model_name
		  FROM t_model_alias a JOIN t_model m ON m.id = a.model_id
		 WHERE a.alias_name = ? AND a.model_id <> ?
		 LIMIT 1`, alias, excludeModelID).Scan(&ref.ID, &ref.ModelName)
	if errors.Is(err, sql.ErrNoRows) {
		return modelRef{}, false, nil
	}
	if err != nil {
		return modelRef{}, false, apierr.New(apierr.CodeInternalError).WithCause(err)
	}
	return ref, true, nil
}

// modelNameOwner finds a model whose public name equals value. Used both when
// validating an alias (an alias may not take another model's public name) and
// when renaming a model (a new name may not take another model's alias).
func modelNameOwner(ctx context.Context, q modelQueryer, value string, excludeModelID int64) (modelRef, bool, *apierr.APIError) {
	var ref modelRef
	err := q.QueryRowContext(ctx, `
		SELECT id, model_name FROM t_model
		 WHERE model_name = ? AND id <> ? LIMIT 1`, value, excludeModelID).
		Scan(&ref.ID, &ref.ModelName)
	if errors.Is(err, sql.ErrNoRows) {
		return modelRef{}, false, nil
	}
	if err != nil {
		return modelRef{}, false, apierr.New(apierr.CodeInternalError).WithCause(err)
	}
	return ref, true, nil
}

// modelNameAvailable is the rename check (F22-1 / F22-2).
func modelNameAvailable(ctx context.Context, q modelQueryer, name string, modelID int64) *apierr.APIError {
	ref, found, apiErr := modelNameOwner(ctx, q, name, modelID)
	if apiErr != nil {
		return apiErr
	}
	if found {
		return apierr.Newf(apierr.CodeResourceStateConflict,
			fmt.Sprintf("模型名 %s 已被模型 %s（id=%d）占用", name, ref.ModelName, ref.ID))
	}
	ref, found, apiErr = aliasOwner(ctx, q, name, modelID)
	if apiErr != nil {
		return apiErr
	}
	if found {
		return aliasClashesWithName(name, ref)
	}
	return nil
}

func aliasTaken(alias string, ref modelRef) *apierr.APIError {
	return apierr.Newf(apierr.CodeResourceStateConflict,
		fmt.Sprintf("别名 %s 已被模型 %s（id=%d）占用", alias, ref.ModelName, ref.ID))
}

func aliasClashesWithName(alias string, ref modelRef) *apierr.APIError {
	return apierr.Newf(apierr.CodeResourceStateConflict,
		fmt.Sprintf("别名 %s 与模型名 %s（id=%d）冲突", alias, ref.ModelName, ref.ID))
}

// isDuplicateKey reports a MySQL unique-key violation (1062).
func isDuplicateKey(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1062
}
