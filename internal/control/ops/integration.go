package ops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
)

// Tool integration deep links (T33 / D-30).
//
// The five rows are seeded by 0026 and fixed: the operator may change only
// enabled and deep_link_template. tool_key is the URL path value and is never
// accepted from the body, so "create a sixth tool" is not expressible.
//
// A template is a URL with named placeholders. Only {key} / {base_url} /
// {model} are allowed; the server rejects anything else before writing. The
// browser renders the template but never learns a credential (F4-3).

const integrationTemplateMaxRunes = 512

var allowedIntegrationPlaceholders = map[string]bool{
	"key":      true,
	"base_url": true,
	"model":    true,
}

// integrationPlaceholderRE matches one well-formed placeholder. Nested or
// unmatched braces are rejected separately; they would render literally and
// silently produce a broken deep link.
var integrationPlaceholderRE = regexp.MustCompile(`\{[^{}]*\}`)

// integrationView is the operator wire shape. There is no credentials field:
// the table has none, and the public route exposes a subset of this struct.
type integrationView struct {
	ToolKey          string  `json:"tool_key"`
	DisplayName      string  `json:"display_name"`
	Enabled          bool    `json:"enabled"`
	DeepLinkTemplate *string `json:"deep_link_template"`
}

type integrationRow struct {
	ID int64
	integrationView
}

type integrationListResponse struct {
	Items []integrationView `json:"items"`
	Total int64             `json:"total"`
}

type integrationDetailResponse struct {
	Integration integrationView `json:"integration"`
}

// integrationPatchRequest deliberately has no tool_key / display_name field.
// An omitted field and an explicit null both leave deep_link_template alone;
// sending "" clears it. Enabling without a template is refused further down.
type integrationPatchRequest struct {
	Enabled          *bool   `json:"enabled"`
	DeepLinkTemplate *string `json:"deep_link_template"`
}

const integrationSelectColumns = `id, tool_key, display_name, enabled, deep_link_template`

// ListIntegrations implements GET /api/admin/integrations.
func (s *Service) ListIntegrations(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT `+integrationSelectColumns+`
		  FROM t_integration
		 ORDER BY id`)
	if err != nil {
		s.logError(r.Context(), "ops_integration_list_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	defer rows.Close()

	items := make([]integrationView, 0, 5)
	for rows.Next() {
		row, err := scanIntegration(rows)
		if err != nil {
			s.logError(r.Context(), "ops_integration_scan_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		items = append(items, row.integrationView)
	}
	if err := rows.Err(); err != nil {
		s.logError(r.Context(), "ops_integration_rows_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, integrationListResponse{Items: items, Total: int64(len(items))})
}

// UpdateIntegration implements PATCH /api/admin/integrations/{toolKey}.
//
// An unknown tool_key answers 404 resource_not_found rather than creating a
// row: the five tools are product constants (D-30 Q3).
func (s *Service) UpdateIntegration(w http.ResponseWriter, r *http.Request) {
	toolKey := strings.TrimSpace(r.PathValue("toolKey"))
	if toolKey == "" || len([]rune(toolKey)) > 64 {
		mw.WriteError(w, r, mw.InvalidField("tool_key"))
		return
	}
	var req integrationPatchRequest
	if apiErr := decodeJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if req.Enabled == nil && req.DeepLinkTemplate == nil {
		mw.WriteError(w, r, mw.InvalidField("body"))
		return
	}

	ctx := r.Context()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		s.logError(ctx, "ops_integration_begin_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	before, apiErr := loadIntegrationTx(ctx, tx, toolKey)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	next := before.integrationView
	if req.DeepLinkTemplate != nil {
		tpl, apiErr := normalizeIntegrationTemplate(*req.DeepLinkTemplate)
		if apiErr != nil {
			mw.WriteError(w, r, apiErr)
			return
		}
		next.DeepLinkTemplate = tpl
	}
	if req.Enabled != nil {
		next.Enabled = *req.Enabled
	}
	// D-30 Q2 / hard constraint 1: an enabled row with no template would make
	// the site render an empty deep link, so enabling is refused until a
	// template is stored. Disabling keeps the template for a later re-enable.
	if next.Enabled && next.DeepLinkTemplate == nil {
		mw.WriteError(w, r, apierr.Newf(apierr.CodeInvalidRequestBody,
			fmt.Sprintf("开启工具 %s 前必须先配置深链模板", toolKey)))
		return
	}

	now := s.now()
	if _, err := tx.ExecContext(ctx, `
		UPDATE t_integration
		   SET enabled = ?, deep_link_template = ?, updated_at = ?
		 WHERE id = ?`,
		next.Enabled, next.DeepLinkTemplate, now, before.ID); err != nil {
		s.logError(ctx, "ops_integration_update_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	after, apiErr := loadIntegrationTx(ctx, tx, toolKey)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if err := s.insertAudit(ctx, tx, r, "INTEGRATION", "UPDATE", "t_integration", before.ID,
		integrationAuditView(before.integrationView), integrationAuditView(after.integrationView), ""); err != nil {
		s.logError(ctx, "ops_integration_audit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if err := tx.Commit(); err != nil {
		s.logError(ctx, "ops_integration_commit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed = true

	_ = mw.WriteJSON(w, http.StatusOK, integrationDetailResponse{Integration: after.integrationView})
}

// normalizeIntegrationTemplate trims the value, treats an empty string as
// NULL, enforces the 512-rune column bound and the placeholder whitelist.
func normalizeIntegrationTemplate(raw string) (*string, *apierr.APIError) {
	tpl := strings.TrimSpace(raw)
	if tpl == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(tpl) > integrationTemplateMaxRunes {
		return nil, apierr.Newf(apierr.CodeInvalidRequestBody,
			fmt.Sprintf("深链模板最多 %d 个字符，当前 %d 个",
				integrationTemplateMaxRunes, utf8.RuneCountInString(tpl)))
	}
	matches := integrationPlaceholderRE.FindAllString(tpl, -1)
	for _, match := range matches {
		name := match[1 : len(match)-1]
		if !allowedIntegrationPlaceholders[name] {
			return nil, apierr.Newf(apierr.CodeInvalidRequestBody,
				fmt.Sprintf("深链模板包含未允许的占位符 %s；只支持 {key} / {base_url} / {model}", match))
		}
	}
	// Remaining braces after removing the well-formed placeholders mean a
	// nested or unmatched pair. Name the offending fragment so the operator
	// does not have to diff the whole template by eye.
	if strings.Count(tpl, "{") != len(matches) || strings.Count(tpl, "}") != len(matches) {
		fragment := firstUnbalancedBrace(tpl, matches)
		return nil, apierr.Newf(apierr.CodeInvalidRequestBody,
			fmt.Sprintf("深链模板存在未闭合或嵌套的花括号 %s；只支持 {key} / {base_url} / {model}", fragment))
	}
	out := tpl
	return &out, nil
}

// firstUnbalancedBrace returns the shortest text around the first brace that
// is not part of an allowed placeholder, for the error message only.
func firstUnbalancedBrace(tpl string, matches []string) string {
	rest := tpl
	for _, match := range matches {
		rest = strings.Replace(rest, match, "", 1)
	}
	idx := strings.IndexAny(rest, "{}")
	if idx < 0 {
		return "{}"
	}
	end := idx + 1
	// Include the immediately following brace when the pair is adjacent, so
	// the message can show "{key" or "}}" rather than a lone character.
	if end < len(rest) && (rest[end] == '{' || rest[end] == '}') {
		end++
	}
	return rest[idx:end]
}

func scanIntegration(row rowScanner) (integrationRow, error) {
	var (
		row0 integrationRow
		tpl  sql.NullString
	)
	if err := row.Scan(&row0.ID, &row0.ToolKey, &row0.DisplayName, &row0.Enabled, &tpl); err != nil {
		return integrationRow{}, err
	}
	if tpl.Valid {
		v := tpl.String
		row0.DeepLinkTemplate = &v
	}
	return row0, nil
}

func loadIntegrationTx(ctx context.Context, tx *sql.Tx, toolKey string) (integrationRow, *apierr.APIError) {
	row, err := scanIntegration(tx.QueryRowContext(ctx, `
		SELECT `+integrationSelectColumns+`
		  FROM t_integration
		 WHERE tool_key = ? FOR UPDATE`, toolKey))
	if errors.Is(err, sql.ErrNoRows) {
		return integrationRow{}, apierr.New(apierr.CodeResourceNotFound)
	}
	if err != nil {
		return integrationRow{}, apierr.New(apierr.CodeInternalError).WithCause(err)
	}
	return row, nil
}

// integrationAuditView is the before/after JSON. display_name is included so a
// future seed rename is visible in the trail, even though the API cannot
// change it today.
func integrationAuditView(v integrationView) map[string]any {
	return map[string]any{
		"tool_key":           v.ToolKey,
		"display_name":       v.DisplayName,
		"enabled":            v.Enabled,
		"deep_link_template": v.DeepLinkTemplate,
	}
}
