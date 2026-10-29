package ops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/jsonx"
)

// Manual upstream bill import (T40-c / OI-15).
//
// t_upstream_bill had no producer: internal/recon/daily.go read it, nothing
// wrote it, so runExternal returned "no bills for this day" (daily.go:901-905)
// and the upstream segment of the daily reconciliation was a no-op. This
// endpoint is the V1.0 path chosen in card §2 (A: MANUAL_UPLOAD): operations
// paste the upstream bill for one channel and one period and the rows land in
// t_upstream_bill, where the next runExternal matches them.
//
// The import never writes t_usage_record or t_ledger_entry. Reconciliation is a
// read-side comparison; correcting local usage from a bill would erase the
// discrepancy the comparison exists to find.

const (
	// billAdapterManualUpload is the 0016 enum value for this path.
	billAdapterManualUpload = "MANUAL_UPLOAD"

	// maxImportRows bounds one transaction. decodeJSON already caps the body at
	// 256 KiB; the row cap stops a body of tiny rows from opening a bulk
	// transaction whose single failure rolls back thousands of statements.
	maxImportRows = 5000

	// Column widths from 0016_upstream_bill.sql. Checking here turns a MySQL
	// strict-mode truncation error into a 400 that names the offending row.
	maxBillNoRunes          = 64  // bill_no varchar(64)
	maxUpstreamTradeNoRunes = 64  // upstream_trade_no varchar(64)
	maxBillRequestIDRunes   = 36  // request_id char(36)
	maxBillModelNameRunes   = 128 // model_name varchar(128)
	maxBillCurrencyRunes    = 8   // currency varchar(8)

	// tokens_in / tokens_out are int unsigned.
	maxTokenCount = int64(math.MaxUint32)
)

// importUpstreamBillSQL is the idempotent write.
//
// ON DUPLICATE KEY UPDATE is chosen over "skip and count" (the other option in
// card §3.2): an operator re-imports after the provider corrects a line, and a
// skip would leave the stale amount in place, producing a permanent
// AMOUNT_MISMATCH in runExternal until someone edits the row by hand. The
// unique key (channel_id, bill_period, bill_no) keeps the row count at one per
// bill line, so a repeated import cannot double-count.
//
// created_at is deliberately not updated: it records when the line first
// arrived, and t_upstream_bill has no updated_at column (card §4.1 forbids a
// migration for bookkeeping). The re-import itself is recorded in
// t_admin_audit_log.
const importUpstreamBillSQL = `
	INSERT INTO t_upstream_bill
		(channel_id, bill_adapter, bill_period, bill_no, request_id,
		 upstream_trade_no, model_name, tokens_in, tokens_out, amount_micro,
		 currency, raw_row, created_at)
	VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
	ON DUPLICATE KEY UPDATE
		request_id        = VALUES(request_id),
		upstream_trade_no = VALUES(upstream_trade_no),
		model_name        = VALUES(model_name),
		tokens_in         = VALUES(tokens_in),
		tokens_out        = VALUES(tokens_out),
		amount_micro      = VALUES(amount_micro),
		currency          = VALUES(currency),
		raw_row           = VALUES(raw_row)`

type upstreamBillImportRequest struct {
	ChannelID  *int64             `json:"channel_id"`
	BillPeriod *string            `json:"bill_period"`
	Rows       []jsonx.RawMessage `json:"rows"`
}

// upstreamBillImportRawRow mirrors the writable columns as raw JSON. Keeping
// each field raw means an unknown upstream column neither breaks the decode nor
// gets dropped: raw_row stores the row verbatim (card §3.3).
type upstreamBillImportRawRow struct {
	BillNo          jsonx.RawMessage `json:"bill_no"`
	RequestID       jsonx.RawMessage `json:"request_id"`
	UpstreamTradeNo jsonx.RawMessage `json:"upstream_trade_no"`
	ModelName       jsonx.RawMessage `json:"model_name"`
	TokensIn        jsonx.RawMessage `json:"tokens_in"`
	TokensOut       jsonx.RawMessage `json:"tokens_out"`
	AmountMicro     jsonx.RawMessage `json:"amount_micro"`
	Currency        jsonx.RawMessage `json:"currency"`
}

// upstreamBillImportRow is one validated row. request_id / upstream_trade_no /
// model_name are `any` because the columns are nullable and an absent value
// must be SQL NULL, not the empty string.
type upstreamBillImportRow struct {
	billNo          string
	requestID       any
	upstreamTradeNo any
	modelName       any
	tokensIn        int64
	tokensOut       int64
	amountMicro     int64
	currency        string
	rawRow          []byte
}

// upstreamBillImportResponse reports what the write did. inserted/updated/
// unchanged come from MySQL's affected-row count for the upsert.
type upstreamBillImportResponse struct {
	ChannelID  int64  `json:"channel_id"`
	BillPeriod string `json:"bill_period"`
	Received   int    `json:"received"`
	Inserted   int    `json:"inserted"`
	Updated    int    `json:"updated"`
	Unchanged  int    `json:"unchanged"`
}

// ImportUpstreamBills implements POST /api/admin/upstream-bills/import.
//
// All rows are validated before the transaction opens, so a bad row at index N
// leaves the table untouched instead of importing 1..N-1.
func (s *Service) ImportUpstreamBills(w http.ResponseWriter, r *http.Request) {
	var req upstreamBillImportRequest
	if apiErr := decodeJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	channelID, billPeriod, rows, apiErr := validateUpstreamBillImport(&req)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	ctx := r.Context()
	now := s.now()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		s.logError(ctx, "ops_upstream_bill_begin_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	exists, err := channelExistsTx(ctx, tx, channelID)
	if err != nil {
		s.logError(ctx, "ops_upstream_bill_channel_check_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if !exists {
		mw.WriteError(w, r, apierr.Newf(apierr.CodeInvalidRequestBody,
			fmt.Sprintf("channel_id=%d 不存在或已删除", channelID)))
		return
	}

	var inserted, updated, unchanged int
	for _, row := range rows {
		res, err := tx.ExecContext(ctx, importUpstreamBillSQL,
			channelID, billAdapterManualUpload, billPeriod, row.billNo, row.requestID,
			row.upstreamTradeNo, row.modelName, row.tokensIn, row.tokensOut,
			row.amountMicro, row.currency, row.rawRow, now)
		if err != nil {
			s.logError(ctx, "ops_upstream_bill_insert_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		affected, err := res.RowsAffected()
		if err != nil {
			s.logError(ctx, "ops_upstream_bill_affected_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		// MySQL reports 1 for an insert, 2 for a duplicate that changed at
		// least one column and 0 for a duplicate whose values are identical.
		// The DSNs in this repository do not set clientFoundRows, which would
		// report 1 for the identical case.
		switch affected {
		case 1:
			inserted++
		case 2:
			updated++
		default:
			unchanged++
		}
	}

	// target_id is the channel: one import span many bill rows, and the channel
	// is the object an operator filters the trail by.
	after := map[string]any{
		"channel_id":  channelID,
		"bill_period": billPeriod,
		"received":    len(rows),
		"inserted":    inserted,
		"updated":     updated,
		"unchanged":   unchanged,
	}
	if err := s.insertAudit(ctx, tx, r, "UPSTREAM_BILL", "IMPORT", "t_upstream_bill",
		channelID, nil, after, ""); err != nil {
		s.logError(ctx, "ops_upstream_bill_audit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if err := tx.Commit(); err != nil {
		s.logError(ctx, "ops_upstream_bill_commit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed = true

	_ = mw.WriteJSON(w, http.StatusOK, upstreamBillImportResponse{
		ChannelID:  channelID,
		BillPeriod: billPeriod,
		Received:   len(rows),
		Inserted:   inserted,
		Updated:    updated,
		Unchanged:  unchanged,
	})
}

// ---------------------------------------------------------------------------
// validation
// ---------------------------------------------------------------------------

func validateUpstreamBillImport(req *upstreamBillImportRequest) (int64, string, []upstreamBillImportRow, *apierr.APIError) {
	if req.ChannelID == nil || *req.ChannelID <= 0 {
		return 0, "", nil, mw.InvalidField("channel_id")
	}
	if req.BillPeriod == nil {
		return 0, "", nil, mw.InvalidField("bill_period")
	}
	billPeriod, apiErr := parseBillPeriod(*req.BillPeriod)
	if apiErr != nil {
		return 0, "", nil, apiErr
	}
	if len(req.Rows) == 0 {
		return 0, "", nil, mw.InvalidField("rows")
	}
	if len(req.Rows) > maxImportRows {
		return 0, "", nil, apierr.Newf(apierr.CodeInvalidRequestBody,
			fmt.Sprintf("rows 一次最多 %d 行，收到 %d 行", maxImportRows, len(req.Rows)))
	}

	rows := make([]upstreamBillImportRow, 0, len(req.Rows))
	for i, raw := range req.Rows {
		row, apiErr := parseUpstreamBillRow(raw, fmt.Sprintf("rows[%d]", i))
		if apiErr != nil {
			return 0, "", nil, apiErr
		}
		rows = append(rows, row)
	}
	return *req.ChannelID, billPeriod, rows, nil
}

// parseBillPeriod accepts the two day forms loadBills compares against.
//
// daily.go:1056-1062 queries `bill_period IN (bizDate, bizDate without dashes)`,
// so a month form ("2026-10") — allowed by the 0016 column comment — would be
// written successfully and then never seen by runExternal. Rejecting it here is
// deliberate: an import that cannot be reconciled is worse than a 400.
func parseBillPeriod(raw string) (string, *apierr.APIError) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", mw.InvalidField("bill_period")
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t.Format("2006-01-02"), nil
	}
	if t, err := time.Parse("20060102", v); err == nil && t.Format("20060102") == v {
		return v, nil
	}
	return "", apierr.Newf(apierr.CodeInvalidRequestBody,
		"bill_period 只接受 YYYY-MM-DD 或 YYYYMMDD（对账按日账期读取）")
}

func parseUpstreamBillRow(raw jsonx.RawMessage, path string) (upstreamBillImportRow, *apierr.APIError) {
	var out upstreamBillImportRow
	var src upstreamBillImportRawRow
	if err := jsonx.Unmarshal(raw, &src); err != nil {
		return out, mw.InvalidField(path)
	}

	billNo, ok, apiErr := rowString(src.BillNo, path+".bill_no")
	if apiErr != nil {
		return out, apiErr
	}
	if !ok || billNo == "" {
		return out, mw.InvalidField(path + ".bill_no")
	}
	if len([]rune(billNo)) > maxBillNoRunes {
		return out, mw.InvalidField(path + ".bill_no")
	}
	out.billNo = billNo

	requestID, ok, apiErr := rowString(src.RequestID, path+".request_id")
	if apiErr != nil {
		return out, apiErr
	}
	if len([]rune(requestID)) > maxBillRequestIDRunes {
		return out, mw.InvalidField(path + ".request_id")
	}
	out.requestID = nullableImportString(requestID, ok)

	tradeNo, ok, apiErr := rowString(src.UpstreamTradeNo, path+".upstream_trade_no")
	if apiErr != nil {
		return out, apiErr
	}
	if len([]rune(tradeNo)) > maxUpstreamTradeNoRunes {
		return out, mw.InvalidField(path + ".upstream_trade_no")
	}
	out.upstreamTradeNo = nullableImportString(tradeNo, ok)

	modelName, ok, apiErr := rowString(src.ModelName, path+".model_name")
	if apiErr != nil {
		return out, apiErr
	}
	if len([]rune(modelName)) > maxBillModelNameRunes {
		return out, mw.InvalidField(path + ".model_name")
	}
	out.modelName = nullableImportString(modelName, ok)

	if out.tokensIn, apiErr = rowToken(src.TokensIn, path+".tokens_in"); apiErr != nil {
		return out, apiErr
	}
	if out.tokensOut, apiErr = rowToken(src.TokensOut, path+".tokens_out"); apiErr != nil {
		return out, apiErr
	}

	amount, apiErr := rowInt64(src.AmountMicro, path+".amount_micro")
	if apiErr != nil {
		return out, apiErr
	}
	if amount == nil {
		return out, mw.InvalidField(path + ".amount_micro")
	}
	if *amount < 0 {
		return out, apierr.Newf(apierr.CodeInvalidRequestBody,
			path+".amount_micro 必须 ≥ 0（int64 微积分）")
	}
	out.amountMicro = *amount

	currency, ok, apiErr := rowString(src.Currency, path+".currency")
	if apiErr != nil {
		return out, apiErr
	}
	if !ok || currency == "" {
		currency = "CNY"
	}
	currency = strings.ToUpper(currency)
	if len([]rune(currency)) > maxBillCurrencyRunes {
		return out, mw.InvalidField(path + ".currency")
	}
	out.currency = currency

	// Verbatim bytes: every upstream column survives, including the ones this
	// endpoint does not use (card §3.3).
	out.rawRow = append([]byte(nil), raw...)
	return out, nil
}

func rowToken(raw jsonx.RawMessage, path string) (int64, *apierr.APIError) {
	v, apiErr := rowInt64(raw, path)
	if apiErr != nil {
		return 0, apiErr
	}
	if v == nil {
		return 0, nil
	}
	if *v < 0 {
		return 0, apierr.Newf(apierr.CodeInvalidRequestBody,
			path+" 必须 ≥ 0（int unsigned）")
	}
	if *v > maxTokenCount {
		return 0, apierr.Newf(apierr.CodeInvalidRequestBody,
			fmt.Sprintf("%s 超过 int unsigned 上限 %d", path, maxTokenCount))
	}
	return *v, nil
}

// rowString returns the trimmed string and whether the field was present. A
// JSON null counts as absent.
func rowString(raw jsonx.RawMessage, path string) (string, bool, *apierr.APIError) {
	if isJSONAbsent(raw) {
		return "", false, nil
	}
	var v string
	if err := jsonx.Unmarshal(raw, &v); err != nil {
		return "", false, mw.InvalidField(path)
	}
	return strings.TrimSpace(v), true, nil
}

// rowInt64 decodes an integer field. A float (1.5, 12.0, 1e3) and a string both
// fail the decode and become 400: amount_micro and the token counts are
// integers, so a float is refused rather than rounded (D-07).
func rowInt64(raw jsonx.RawMessage, path string) (*int64, *apierr.APIError) {
	if isJSONAbsent(raw) {
		return nil, nil
	}
	var v int64
	if err := jsonx.Unmarshal(raw, &v); err != nil {
		return nil, apierr.Newf(apierr.CodeInvalidRequestBody,
			path+" 必须是整数（不接受浮点或字符串）")
	}
	return &v, nil
}

func isJSONAbsent(raw jsonx.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

// nullableImportString maps an empty value to SQL NULL: the three text columns
// are nullable and "" would make the approximate match key ("") collide with a
// genuinely empty model name.
func nullableImportString(v string, present bool) any {
	if !present || v == "" {
		return nil
	}
	return v
}

func channelExistsTx(ctx context.Context, tx *sql.Tx, id int64) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM t_channel WHERE id = ? AND deleted_at IS NULL`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
