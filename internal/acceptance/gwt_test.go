package acceptance

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/billing"
	"yunshu.dev/core/internal/config"
	"yunshu.dev/core/internal/gw"
	"yunshu.dev/core/internal/jobs"
	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/ledger"
	"yunshu.dev/core/internal/platform/clock"
	"yunshu.dev/core/internal/ratelimit"
	"yunshu.dev/core/internal/redisx"
	"yunshu.dev/core/internal/settle"
	"yunshu.dev/relaykit/ir"
)

// G-01 — one external request settles once; a retried upstream attempt is not
// a second charge. Values are the ones in PRD appendix D.
func TestGWT_G01_BillingOnceAndAttemptRows(t *testing.T) {
	uid := newUser(t, userOpts{overdraft: 500000})
	setAccount(t, uid, 1000000, 0)
	addBatch(t, uid, batchOpts{typ: ledger.BatchTypeRecharge, face: 1000000, remaining: 1000000})
	store := ledger.NewStore(testDB, clock.Real{})
	now := time.Now().UTC()
	requestID := rid()

	pre, err := store.Prehold(context.Background(), ledger.PreholdParams{
		RequestID: requestID, UserID: uid, APIKeyID: 1, ModelID: 1, NodeID: "gwt",
		RawEstMicro: 300000, EstMicro: 300000, MaxHoldRatioBP: 5000,
		ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("G-01: prehold: %v", err)
	}
	if pre.HoldMicro != 300000 {
		t.Fatalf("G-01: hold=%d, want 300000", pre.HoldMicro)
	}

	cost := billing.CostMicro(1200, 800, 1500, 4500)
	if cost != 5400 {
		t.Fatalf("G-01: cost=%d, want 5400 ((1200*1500 + 800*4500 + 999)/1000)", cost)
	}
	res, err := store.Settle(context.Background(), ledger.SettleParams{
		RequestID: requestID, UserID: uid, ConsumedMicro: cost,
	})
	if err != nil {
		t.Fatalf("G-01: settle: %v", err)
	}
	if res.ConsumedMicro != 5400 {
		t.Fatalf("G-01: settled=%d, want 5400", res.ConsumedMicro)
	}
	if bal := accountBalance(t, uid); bal != 994600 {
		t.Fatalf("G-01: balance=%d, want 994600", bal)
	}

	writer := settle.NewUsageWriter(testDB, nil, clock.Real{})
	writer.WriteUsage(settle.UsageRecord{
		RequestID: requestID, UserID: uid, APIKeyID: 1, ModelID: 1, ModelName: "m",
		RequestModel: "m", InboundProto: "OPENAI_COMPAT", HTTPStatus: 200,
		Result: "SUCCESS", TokensIn: 1200, TokensOut: 800,
		ConsumedMicro: 5400, UpstreamAttempts: 2, TotalMS: 10, CreatedAt: now,
	})
	writer.WriteAttempt(settle.AttemptRecord{
		RequestID: requestID, AttemptIndex: 0, UserID: uid, ModelID: 1, RouteID: 1,
		ChannelID: 1, ChannelName: "ch", ProtocolCode: "OPENAI_COMPAT",
		UpstreamModel: "m", Phase: "UPSTREAM", Result: "FAILED", HTTPStatus: 504,
		IsRetryable: true, RetryDecision: "RETRY", TotalMS: 5, TokensIn: 0, TokensOut: 0,
		CreatedAt: now,
	})
	writer.WriteAttempt(settle.AttemptRecord{
		RequestID: requestID, AttemptIndex: 1, UserID: uid, ModelID: 1, RouteID: 1,
		ChannelID: 1, ChannelName: "ch", ProtocolCode: "OPENAI_COMPAT",
		UpstreamModel: "m", Phase: "UPSTREAM", Result: "SUCCESS", HTTPStatus: 200,
		TotalMS: 5, TokensIn: 1200, TokensOut: 800, CreatedAt: now,
	})
	if err := writer.Flush(context.Background()); err != nil {
		t.Fatalf("G-01: flush usage: %v", err)
	}

	var rows int
	var consumed int64
	var attempts int64
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT COUNT(*), COALESCE(MAX(consumed_micro), 0), COALESCE(MAX(upstream_attempts), 0)
		  FROM t_usage_record WHERE request_id = ?`, requestID).
		Scan(&rows, &consumed, &attempts); err != nil {
		t.Fatalf("G-01: read usage: %v", err)
	}
	if rows != 1 || consumed != 5400 || attempts != 2 {
		t.Fatalf("G-01: usage rows=%d consumed=%d attempts=%d, want 1/5400/2", rows, consumed, attempts)
	}

	var attemptRows int
	var failedIn, failedOut int64
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM t_upstream_attempt WHERE request_id = ?`, requestID).Scan(&attemptRows); err != nil {
		t.Fatalf("G-01: count attempts: %v", err)
	}
	if attemptRows != 2 {
		t.Fatalf("G-01: attempt rows=%d, want 2", attemptRows)
	}
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT tokens_in, tokens_out FROM t_upstream_attempt
		 WHERE request_id = ? AND result = 'FAILED'`, requestID).Scan(&failedIn, &failedOut); err != nil {
		t.Fatalf("G-01: read failed attempt: %v", err)
	}
	if failedIn != 0 || failedOut != 0 {
		t.Fatalf("G-01: failed attempt tokens=%d/%d, want 0/0", failedIn, failedOut)
	}

	header := httptest.NewRecorder()
	apierr.SetResponseHeaders(header, apierr.ResponseMeta{
		RequestID: requestID, CreditsConsumed: &res.ConsumedMicro,
	})
	if got := header.Header().Get("X-Credits-Consumed"); got != "5400" {
		t.Fatalf("G-01: X-Credits-Consumed=%q, want 5400 (DB value)", got)
	}

	// Both attempts fail: consumed 0, balance unchanged, request id still set.
	balanceBefore := accountBalance(t, uid)
	failID := rid()
	if _, err := store.Prehold(context.Background(), ledger.PreholdParams{
		RequestID: failID, UserID: uid, APIKeyID: 1, ModelID: 1, NodeID: "gwt",
		RawEstMicro: 3000, EstMicro: 3000, MaxHoldRatioBP: 5000,
		ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("G-01: failed-request prehold: %v", err)
	}
	if _, err := store.Settle(context.Background(), ledger.SettleParams{
		RequestID: failID, UserID: uid, ConsumedMicro: 0, ReleaseReason: "UPSTREAM_FAIL",
	}); err != nil {
		t.Fatalf("G-01: release: %v", err)
	}
	if got := accountBalance(t, uid); got != balanceBefore {
		t.Fatalf("G-01: failed request changed balance %d -> %d", balanceBefore, got)
	}
	failHeader := httptest.NewRecorder()
	apierr.SetResponseHeaders(failHeader, apierr.ResponseMeta{RequestID: failID})
	if failHeader.Header().Get("X-Request-Id") != failID {
		t.Fatalf("G-01: failed request has no X-Request-Id")
	}
}

// G-02 — admission compares raw_est, prehold uses est (capped at 50% of the
// available balance). The boundary balance=6500 / raw_est=6000 is the only
// case that distinguishes the two implementations.
func TestGWT_G02_AdmissionUsesRawEstimate(t *testing.T) {
	if got, err := billing.EstimateMicro(1000, 1000, 1500, 4500, 10000, 12000); err != nil || got != 7200 {
		t.Fatalf("G-02: documented est = %d (%v), want 7200 (PRD D: raw 6000 × 1.2)", got, err)
	}

	uid := newUser(t, userOpts{})
	setAccount(t, uid, 6500, 0)
	store := ledger.NewStore(testDB, clock.Real{})
	deps := &gw.Deps{
		Prices:  priceStub{rule: gw.PriceRule{ModelType: "TEXT", InPriceMicroPerK: 1500, OutPriceMicroPerK: 4500}},
		Balance: balanceStub{available: 6500},
		Tokens:  fixedCounter{1000},
		Ledger:  store,
		Clock:   redisx.NewClock(nil, nil),
	}
	maxTokens := int64(1000)
	c := &gw.Ctx{
		RequestID: rid(),
		Endpoint:  gw.EndpointChat,
		Model: &gw.ModelEntry{
			ID: 1, ModelType: "TEXT", ContextLength: 100000, MaxOutputTokens: 4096,
		},
		Auth: &gw.AuthContext{UserID: uid, KeyID: 1, GroupCode: "standard", GroupRatioBP: 10000},
		IR:   &ir.Request{Model: "gwt", MaxTokens: &maxTokens},
	}
	if err := gw.NewEstimateStage(deps).Run(context.Background(), c); err != nil {
		t.Fatalf("G-02: 6500/6000/7200 must be admitted, got %v", err)
	}
	if c.Est.RawMicro != 6000 {
		t.Fatalf("G-02: raw_est=%d, want 6000", c.Est.RawMicro)
	}
	if c.Est.EstMicro <= 6500 {
		t.Fatalf("G-02: est=%d must exceed available 6500 for the boundary to be meaningful", c.Est.EstMicro)
	}
	if err := gw.NewPreholdStage(deps).Run(context.Background(), c); err != nil {
		t.Fatalf("G-02: prehold after admission: %v", err)
	}
	if c.HoldMicro != 3250 {
		t.Fatalf("G-02: hold=%d, want min(est, 6500×50%%)=3250", c.HoldMicro)
	}
	t.Logf("G-02: raw_est=%d available=6500 est=%d (documented raw 6000 / est 7200) hold=%d",
		c.Est.RawMicro, c.Est.EstMicro, c.HoldMicro)
	var holdDB int64
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT hold_micro FROM t_prehold_hold WHERE request_id = ?`, c.RequestID).Scan(&holdDB); err != nil {
		t.Fatalf("G-02: read hold: %v", err)
	}
	if holdDB != 3250 {
		t.Fatalf("G-02: stored hold=%d, want 3250", holdDB)
	}

	// raw_est > available is 402 even when est would fit the 50% cap.
	poor := newUser(t, userOpts{})
	setAccount(t, poor, 10000, 0)
	deps2 := &gw.Deps{
		Prices:  priceStub{rule: gw.PriceRule{ModelType: "TEXT", InPriceMicroPerK: 1500, OutPriceMicroPerK: 4500}},
		Balance: balanceStub{available: 10000},
		Tokens:  fixedCounter{20000},
		Ledger:  store,
		Clock:   redisx.NewClock(nil, nil),
	}
	maxOut := int64(5000)
	poorID := rid()
	c2 := &gw.Ctx{
		RequestID: poorID, Endpoint: gw.EndpointChat,
		Model: &gw.ModelEntry{ID: 1, ModelType: "TEXT", ContextLength: 200000, MaxOutputTokens: 8192},
		Auth:  &gw.AuthContext{UserID: poor, KeyID: 1, GroupCode: "standard", GroupRatioBP: 10000},
		IR:    &ir.Request{Model: "gwt", MaxTokens: &maxOut},
	}
	err := gw.NewEstimateStage(deps2).Run(context.Background(), c2)
	if err == nil {
		t.Fatalf("G-02: raw_est 52500 > available 10000 must be 402")
	}
	var se *gw.StageError
	if !errors.As(err, &se) || se.API.Code != apierr.CodeInsufficientCredits {
		t.Fatalf("G-02: refusal=%v, want insufficient_credits", err)
	}
	var attempts int
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_upstream_attempt WHERE request_id = ?`, poorID).Scan(&attempts); err != nil {
		t.Fatalf("G-02: count attempts: %v", err)
	}
	if attempts != 0 {
		t.Fatalf("G-02: 402 still issued %d upstream attempts", attempts)
	}
	if got := accountBalance(t, poor); got != 10000 {
		t.Fatalf("G-02: 402 changed the balance to %d", got)
	}

	// Small negative balance: 3000, hold 1500, actual 3500 => -500; recharge
	// 5000 => 4500.
	negative := newUser(t, userOpts{})
	setAccount(t, negative, 3000, 0)
	addBatch(t, negative, batchOpts{typ: ledger.BatchTypeRecharge, face: 3000, remaining: 3000})
	negID := rid()
	pre, err := store.Prehold(context.Background(), ledger.PreholdParams{
		RequestID: negID, UserID: negative, APIKeyID: 1, ModelID: 1, NodeID: "gwt",
		RawEstMicro: 3000, EstMicro: 3500, MaxHoldRatioBP: 5000,
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil || pre.HoldMicro != 1500 {
		t.Fatalf("G-02: negative-case hold=%+v err=%v, want 1500", pre, err)
	}
	if _, err := store.Settle(context.Background(), ledger.SettleParams{
		RequestID: negID, UserID: negative, ConsumedMicro: 3500,
	}); err != nil {
		t.Fatalf("G-02: settle negative: %v", err)
	}
	if got := accountBalance(t, negative); got != -500 {
		t.Fatalf("G-02: balance=%d, want -500", got)
	}
	if _, err := store.Recharge(context.Background(), ledger.RechargeParams{
		UserID: negative, AmountMicro: 5000, RechargeOrderID: uniqueOrderID(),
	}); err != nil {
		t.Fatalf("G-02: recharge: %v", err)
	}
	if got := accountBalance(t, negative); got != 4500 {
		t.Fatalf("G-02: balance after recharge=%d, want 4500", got)
	}

	// Hard truncation: available 400,000 + overdraft 500,000 = 900,000 cap;
	// the 1,100,000 excess goes to t_arrears and the account freezes.
	trunc := newUser(t, userOpts{overdraft: 500000})
	setAccount(t, trunc, 400000, 0)
	addBatch(t, trunc, batchOpts{typ: ledger.BatchTypeRecharge, face: 400000, remaining: 400000})
	truncID := rid()
	if _, err := store.Prehold(context.Background(), ledger.PreholdParams{
		RequestID: truncID, UserID: trunc, APIKeyID: 1, ModelID: 1, NodeID: "gwt",
		RawEstMicro: 400000, EstMicro: 400000, MaxHoldRatioBP: 5000,
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("G-02: truncation prehold: %v", err)
	}
	tr, err := store.Settle(context.Background(), ledger.SettleParams{
		RequestID: truncID, UserID: trunc, ConsumedMicro: 2000000,
	})
	if err != nil {
		t.Fatalf("G-02: truncation settle: %v", err)
	}
	if tr.ConsumedMicro != 900000 || tr.ArrearsMicro != 1100000 {
		t.Fatalf("G-02: deducted=%d arrears=%d, want 900000/1100000", tr.ConsumedMicro, tr.ArrearsMicro)
	}
	if tr.ConsumedMicro+tr.ArrearsMicro != 2000000 {
		t.Fatalf("G-02: 900000 + 1100000 != 2000000")
	}
	if got := accountBalance(t, trunc); got != -500000 {
		t.Fatalf("G-02: truncated balance=%d, want -500000", got)
	}
	if status := userStatus(t, trunc); status != "FROZEN_OVERDRAFT" {
		t.Fatalf("G-02: status=%s, want FROZEN_OVERDRAFT", status)
	}
	if _, err := store.Recharge(context.Background(), ledger.RechargeParams{
		UserID: trunc, AmountMicro: 2000000, RechargeOrderID: uniqueOrderID(),
	}); err != nil {
		t.Fatalf("G-02: recovery recharge: %v", err)
	}
	if got := accountBalance(t, trunc); got != 400000 {
		t.Fatalf("G-02: balance after arrears offset=%d, want 400000", got)
	}
	if status := userStatus(t, trunc); status != "NORMAL" {
		t.Fatalf("G-02: status after recharge=%s, want NORMAL", status)
	}
}

// ---------------------------------------------------------------------------
// G-01 / G-02 helpers
// ---------------------------------------------------------------------------

type priceStub struct{ rule gw.PriceRule }

func (s priceStub) GetStandard(ctx context.Context, modelID int64) (jsonx.RawMessage, error) {
	return jsonx.Marshal(s.rule)
}

type balanceStub struct{ available int64 }

func (s balanceStub) Get(ctx context.Context, userID int64) (int64, error) { return s.available, nil }

type fixedCounter struct{ tokens int64 }

func (c fixedCounter) CountInput(text string) (int64, error) { return c.tokens, nil }

func accountBalance(t *testing.T, uid int64) int64 {
	t.Helper()
	var v int64
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT balance_micro FROM t_credit_account WHERE user_id = ?`, uid).Scan(&v); err != nil {
		t.Fatalf("read balance uid=%d: %v", uid, err)
	}
	return v
}

func userStatus(t *testing.T, uid int64) string {
	t.Helper()
	var status string
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT status FROM t_user WHERE id = ?`, uid).Scan(&status); err != nil {
		t.Fatalf("read user status uid=%d: %v", uid, err)
	}
	return status
}

func uniqueOrderID() int64 {
	return int64(time.Now().UnixNano()%1e15) + int64(nextInt()%1000)
}

var _ = sql.ErrNoRows
var _ = strconv.Itoa
var _ = strings.Contains

// G-03 — a payment callback is idempotent and the plan grant is split into a
// recharge batch (no expiry) and a bonus batch (+90 days).
func TestGWT_G03_CallbackIdempotentAndPlanGrant(t *testing.T) {
	plan := planID(t, "STANDARD")
	uid := newUser(t, userOpts{planID: &plan})
	order := createOrder(t, uid, plan)
	if order.PayAmountFen != 9900 || order.RechargeMicro != 99000000 || order.BonusMicro != 20000000 {
		t.Fatalf("G-03: order = %+v, want 9900 fen / 99,000,000 + 20,000,000 micro", order)
	}

	tradeNo := "WX" + strconv.FormatInt(time.Now().UnixNano(), 10)
	var credited int
	for i := 0; i < 3; i++ {
		rec := payCallback(t, order.OrderNo, tradeNo, order.PayAmountFen, paySecret)
		requireStatus(t, "G-03 callback", rec, http.StatusOK)
		var body struct {
			Credited bool `json:"credited"`
			Replayed bool `json:"replayed"`
		}
		decodeBody(t, rec, &body)
		if body.Credited && !body.Replayed {
			credited++
		}
	}
	if credited != 1 {
		t.Fatalf("G-03: %d callbacks credited, want exactly 1", credited)
	}

	// PRD G-03 writes state SUCCESS; the T13 schema/enum uses PAID. Record the
	// deviation instead of hiding it behind a loosely written assertion.
	var state string
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT state FROM t_recharge_order WHERE id = ?`, order.ID).Scan(&state); err != nil {
		t.Fatalf("G-03: read order: %v", err)
	}
	if state != "PAID" {
		t.Fatalf("G-03: order state=%s, want PAID (PRD text says SUCCESS)", state)
	}
	t.Logf("G-03 DEVIATION: PRD appendix D writes order state SUCCESS; t_recharge_order uses PAID")

	if got := accountBalance(t, uid); got != 119000000 {
		t.Fatalf("G-03: balance=%d, want 119,000,000", got)
	}

	var entries, rechargeRows, bonusRows int
	var rechargeMicro, bonusMicro int64
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT COUNT(*),
		       SUM(CASE WHEN amount_recharge_micro > 0 THEN 1 ELSE 0 END),
		       SUM(CASE WHEN amount_bonus_micro > 0 THEN 1 ELSE 0 END),
		       COALESCE(SUM(amount_recharge_micro), 0),
		       COALESCE(SUM(amount_bonus_micro), 0)
		  FROM t_ledger_entry WHERE recharge_order_id = ?`, order.ID).
		Scan(&entries, &rechargeRows, &bonusRows, &rechargeMicro, &bonusMicro); err != nil {
		t.Fatalf("G-03: ledger entries: %v", err)
	}
	if entries != 2 || rechargeRows != 1 || bonusRows != 1 ||
		rechargeMicro != 99000000 || bonusMicro != 20000000 {
		t.Fatalf("G-03: entries=%d recharge=%d/%d bonus=%d/%d, want 2 split rows",
			entries, rechargeRows, rechargeMicro, bonusRows, bonusMicro)
	}

	var rechargeExpire sql.NullTime
	var bonusExpire sql.NullTime
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT MAX(CASE WHEN batch_type = 'RECHARGE' THEN expire_at END),
		       MAX(CASE WHEN batch_type = 'BONUS' THEN expire_at END)
		  FROM t_credit_batch WHERE user_id = ? AND source_id = ?`, uid, order.ID).
		Scan(&rechargeExpire, &bonusExpire); err != nil {
		t.Fatalf("G-03: batches: %v", err)
	}
	if rechargeExpire.Valid {
		t.Fatalf("G-03: recharge batch expire_at=%v, want NULL", rechargeExpire.Time)
	}
	if !bonusExpire.Valid {
		t.Fatalf("G-03: bonus batch has no expire_at")
	}
	if d := bonusExpire.Time.Sub(time.Now().UTC()); d < 89*24*time.Hour || d > 91*24*time.Hour {
		t.Fatalf("G-03: bonus expiry in %v, want ≈90 days", d)
	}

	var rpm, tpm, conc, daily int64
	var source string
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT source, rpm, tpm, concurrency_quota, COALESCE(daily_quota_micro, 0)
		  FROM t_rate_limit_policy
		 WHERE scope_type = 'USER' AND scope_id = ?
		   AND effective_from <= UTC_TIMESTAMP(3)
		   AND (effective_to IS NULL OR effective_to > UTC_TIMESTAMP(3))
		 ORDER BY id DESC LIMIT 1`, uid).Scan(&source, &rpm, &tpm, &conc, &daily); err != nil {
		t.Fatalf("G-03: plan policy: %v", err)
	}
	if rpm != 300 || tpm != 630000 || conc != 110 || daily != 50000000 {
		t.Fatalf("G-03: plan policy %s = %d/%d/%d/%d, want 300/630000/110/50,000,000",
			source, rpm, tpm, conc, daily)
	}

	// Bad signature: nothing credited and the order stays PENDING.
	badUser := newUser(t, userOpts{planID: &plan})
	badOrder := createOrder(t, badUser, plan)
	bad := payCallback(t, badOrder.OrderNo, "WXBAD"+strconv.FormatInt(time.Now().UnixNano(), 10),
		badOrder.PayAmountFen, "wrong-secret")
	requireCode(t, "G-03 bad signature", bad, http.StatusBadRequest, "invalid_request_body")
	var badState string
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT state FROM t_recharge_order WHERE id = ?`, badOrder.ID).Scan(&badState); err != nil {
		t.Fatalf("G-03: read bad order: %v", err)
	}
	if badState != "PENDING" || accountBalance(t, badUser) != 0 {
		t.Fatalf("G-03: bad signature state=%s balance=%d, want PENDING/0", badState, accountBalance(t, badUser))
	}

	// Bonus expiry: an expired BONUS batch is cleared once, with one
	// BATCH_EXPIRE entry, and a rerun is a no-op.
	expUser := newUser(t, userOpts{})
	setAccount(t, expUser, 0, 1000)
	past := time.Now().UTC().Add(-time.Hour)
	batchID := addBatch(t, expUser, batchOpts{
		typ: ledger.BatchTypeBonus, face: 1000, remaining: 1000, expireAt: &past,
	})
	job := jobs.NewExpireBatch(testDB, nil, nil, clock.Real{}, "gwt")
	if res, err := job.Run(context.Background()); err != nil {
		t.Fatalf("G-03: expire batch: %v", err)
	} else if res.Cleared == 0 {
		t.Fatalf("G-03: expire batch cleared nothing (result %+v)", res)
	}
	if got := accountBalance(t, expUser); got != 0 {
		t.Fatalf("G-03: balance after bonus expiry=%d, want 0", got)
	}
	var remaining int64
	var clearedAt sql.NullTime
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT remaining_micro, cleared_at FROM t_credit_batch WHERE id = ?`, batchID).
		Scan(&remaining, &clearedAt); err != nil {
		t.Fatalf("G-03: read expired batch: %v", err)
	}
	if remaining != 0 || !clearedAt.Valid {
		t.Fatalf("G-03: batch remaining=%d cleared_at=%v, want 0/set", remaining, clearedAt.Time)
	}
	if _, err := job.Run(context.Background()); err != nil {
		t.Fatalf("G-03: expire rerun: %v", err)
	}
	var expireEntries int
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_ledger_entry WHERE idem_key = ?`,
		fmt.Sprintf("expire:%d", batchID)).Scan(&expireEntries); err != nil {
		t.Fatalf("G-03: count expire entries: %v", err)
	}
	if expireEntries != 1 {
		t.Fatalf("G-03: BATCH_EXPIRE entries=%d, want 1 after a rerun", expireEntries)
	}

	// Trial falls back to FREE_DEFAULT; a permanent paid plan never does.
	dailyQuota := int64(10000000)
	trial := billing.PlanSpec{Code: billing.PlanTrial, RPM: 60, TPM: 126000, Concurrency: 22, DailyQuotaMicro: &dailyQuota}
	free := billing.PlanSpec{Code: billing.PlanFreeDefault, RPM: 60, TPM: 60000, Concurrency: 5}
	purchased := time.Now().UTC().Add(-8 * 24 * time.Hour)
	expiry := billing.EntitlementExpireAt(trial, purchased, 7)
	trialLimits := billing.EffectiveLimits([]billing.PlanGrant{{
		Plan: trial, PurchasedAt: purchased, EntitlementExpireAt: expiry,
	}}, free, time.Now().UTC())
	if trialLimits.Source != billing.PlanFreeDefault {
		t.Fatalf("G-03: expired trial source=%s, want FREE_DEFAULT", trialLimits.Source)
	}
	paid := billing.PlanSpec{Code: billing.PlanStandard, RPM: 300, TPM: 630000, Concurrency: 110, DailyQuotaMicro: &dailyQuota, Permanent: true}
	paidLimits := billing.EffectiveLimits([]billing.PlanGrant{{
		Plan: paid, PurchasedAt: purchased,
	}}, free, purchased.Add(365*24*time.Hour))
	if paidLimits.Source != billing.PlanStandard || paidLimits.RPM != 300 {
		t.Fatalf("G-03: permanent plan fell back to %+v", paidLimits)
	}
}

type testOrder struct {
	ID            int64  `json:"id"`
	OrderNo       string `json:"order_no"`
	PayAmountFen  int64  `json:"pay_amount_fen"`
	RechargeMicro int64  `json:"recharge_micro"`
	BonusMicro    int64  `json:"bonus_micro"`
}

func createOrder(t *testing.T, uid, plan int64) testOrder {
	t.Helper()
	rec := call(t, uid, http.MethodPost, "/api/console/orders", map[string]any{
		"order_type":  "PLAN",
		"plan_id":     plan,
		"pay_channel": "WECHAT",
	}, callOpts{header: map[string]string{
		"Idempotency-Key": fmt.Sprintf("gwt-order-%d-%d", time.Now().UnixNano(), nextInt()),
	}})
	requireStatus(t, "create order", rec, http.StatusCreated)
	var body struct {
		Order testOrder `json:"order"`
	}
	decodeBody(t, rec, &body)
	if body.Order.OrderNo == "" {
		t.Fatalf("create order: no order_no in %s", rec.Body.String())
	}
	return body.Order
}

func payCallback(t *testing.T, orderNo, tradeNo string, amountFen int64, secret string) *httptest.ResponseRecorder {
	t.Helper()
	return call(t, 0, http.MethodPost, "/api/v1/pay/callback/wechat", map[string]any{
		"out_trade_no": orderNo,
		"trade_no":     tradeNo,
		"amount_fen":   amountFen,
		"status":       "SUCCESS",
		"sign":         signCallback(secret, orderNo, tradeNo, amountFen, "SUCCESS"),
	})
}

// G-04 — the refund base excludes bonus and consumed credits; an ISSUED
// invoice blocks the refund; the minimum is 1,000 credits.
func TestGWT_G04_RefundBaseAndIssuedInvoice(t *testing.T) {
	store := ledger.NewStore(testDB, clock.Real{})
	uid := newUser(t, userOpts{})
	setAccount(t, uid, 79000000, 10000000)
	orderID := newRechargeOrderDB(t, uid, 99000000)
	addBatch(t, uid, batchOpts{typ: ledger.BatchTypeRecharge, face: 99000000, remaining: 79000000, sourceID: &orderID})
	addBatch(t, uid, batchOpts{typ: ledger.BatchTypeBonus, face: 20000000, remaining: 10000000})

	quote, err := store.QuoteRefund(context.Background(), ledger.RefundQuoteParams{
		UserID: uid, RechargeOrderID: &orderID,
	})
	if err != nil {
		t.Fatalf("G-04: quote: %v", err)
	}
	if quote.RefundableMicro != 79000000 || quote.RefundFen != 7110 || quote.FeeFen != 790 {
		t.Fatalf("G-04: refundable=%d fen=%d fee=%d, want 79,000,000 / 7110 / 790",
			quote.RefundableMicro, quote.RefundFen, quote.FeeFen)
	}
	if !quote.Eligible {
		t.Fatalf("G-04: quote ineligible: %s", quote.Reason)
	}

	refundID := newRefundOrderDB(t, uid, orderID)
	res, err := store.Refund(context.Background(), ledger.RefundParams{
		UserID: uid, RechargeOrderID: orderID, RefundOrderID: refundID,
	})
	if err != nil {
		t.Fatalf("G-04: refund: %v", err)
	}
	if res.RefundableMicro != 79000000 || res.RefundFen != 7110 || res.FeeFen != 790 {
		t.Fatalf("G-04: refund result=%+v, want 79,000,000/7110/790", res)
	}
	if res.BalanceAfterMicro != 10000000 || res.BonusRemainingMicro != 10000000 {
		t.Fatalf("G-04: balance=%d bonus=%d, want 10,000,000 bonus only",
			res.BalanceAfterMicro, res.BonusRemainingMicro)
	}
	t.Logf("G-04: refundable=%d micro (7,900 credits) refund=%d fen (71.10 yuan) fee=%d fen (7.90 yuan) balance_after=%d bonus_remaining=%d",
		res.RefundableMicro, res.RefundFen, res.FeeFen, res.BalanceAfterMicro, res.BonusRemainingMicro)

	blocked := newUser(t, userOpts{})
	setAccount(t, blocked, 79000000, 0)
	blockedOrder := newRechargeOrderDB(t, blocked, 79000000)
	addBatch(t, blocked, batchOpts{typ: ledger.BatchTypeRecharge, face: 79000000, remaining: 79000000, sourceID: &blockedOrder})
	newInvoiceDB(t, blocked, blockedOrder, "ISSUED")
	blockedQuote, err := store.QuoteRefund(context.Background(), ledger.RefundQuoteParams{
		UserID: blocked, RechargeOrderID: &blockedOrder,
	})
	if err != nil {
		t.Fatalf("G-04: blocked quote: %v", err)
	}
	if blockedQuote.Eligible || blockedQuote.Reason == "" {
		t.Fatalf("G-04: ISSUED invoice quote=%+v, want rejection with a reason", blockedQuote)
	}

	tiny := newUser(t, userOpts{})
	setAccount(t, tiny, 900000, 0)
	tinyOrder := newRechargeOrderDB(t, tiny, 900000)
	addBatch(t, tiny, batchOpts{typ: ledger.BatchTypeRecharge, face: 900000, remaining: 900000, sourceID: &tinyOrder})
	tinyQuote, err := store.QuoteRefund(context.Background(), ledger.RefundQuoteParams{
		UserID: tiny, RechargeOrderID: &tinyOrder,
	})
	if err != nil {
		t.Fatalf("G-04: tiny quote: %v", err)
	}
	if tinyQuote.Eligible || !(strings.Contains(tinyQuote.Reason, "1,000") || strings.Contains(tinyQuote.Reason, "1000")) {
		t.Fatalf("G-04: 900-credit quote=%+v, want rejection naming the 1,000 minimum", tinyQuote)
	}
}

// G-05 — changing a price does not rewrite historical bills.
func TestGWT_G05_PriceChangeDoesNotRewriteHistory(t *testing.T) {
	ops := newUser(t, userOpts{role: "OPS"})
	user := newUser(t, userOpts{})
	name := modelName()
	modelID := newModel(t, name, "LISTED")
	priceID := createPriceAPI(t, ops, modelID, 10000, 20000, 5000)

	requestID := rid()
	insertUsage(t, requestID, user, modelID, time.Now().UTC().Add(-time.Hour), "SUCCESS", 1234)
	if _, err := testDB.ExecContext(context.Background(),
		`UPDATE t_usage_record SET price_rule_id = ? WHERE request_id = ?`, priceID, requestID); err != nil {
		t.Fatalf("G-05: tag usage price: %v", err)
	}

	raise := call(t, ops, http.MethodPatch, fmt.Sprintf("/api/admin/prices/%d", priceID), map[string]any{
		"in_price_micro_per_k": 12000,
		"effective_from":       time.Now().UTC().Add(3 * 24 * time.Hour).Format(time.RFC3339),
	})
	requireCode(t, "G-05 raise", raise, http.StatusBadRequest, "invalid_request_body")

	lower := call(t, ops, http.MethodPatch, fmt.Sprintf("/api/admin/prices/%d", priceID), map[string]any{
		"in_price_micro_per_k": 9000,
		"effective_from":       time.Now().UTC().Format(time.RFC3339),
	})
	requireStatus(t, "G-05 decrease", lower, http.StatusOK)

	var consumed int64
	var storedRule int64
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT consumed_micro, price_rule_id FROM t_usage_record WHERE request_id = ?`, requestID).
		Scan(&consumed, &storedRule); err != nil {
		t.Fatalf("G-05: read historical usage: %v", err)
	}
	if consumed != 1234 {
		t.Fatalf("G-05: historical consumed=%d, want 1234 (unchanged)", consumed)
	}
	if storedRule != priceID {
		t.Fatalf("G-05: historical price_rule_id=%d, want %d", storedRule, priceID)
	}
	var version int64
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT version FROM t_price_rule WHERE id = ?`, priceID).Scan(&version); err != nil {
		t.Fatalf("G-05: read version: %v", err)
	}
	if version < 2 {
		t.Fatalf("G-05: price version=%d, want >=2", version)
	}
	t.Logf("G-05 DEVIATION: price history is versioned on one row; PRD G-05 describes a separate old-version row")

	publishPrices(t)
	raw, err := (&config.PriceCache{Redis: config.NewGoRedis(testRedis.Raw())}).GetStandard(context.Background(), modelID)
	if err != nil {
		t.Fatalf("G-05: read K19: %v", err)
	}
	rule, err := gw.ParsePrice(raw)
	if err != nil {
		t.Fatalf("G-05: parse K19: %v", err)
	}
	if rule.InPriceMicroPerK != 9000 {
		t.Fatalf("G-05: K19 in price=%d, want 9000", rule.InPriceMicroPerK)
	}
}

// G-06 — the 25% floor uses integer 4/3 arithmetic; a missing cost is a
// warning, never a zero-cost pass.
func TestGWT_G06_GroupRatioFloor(t *testing.T) {
	cost := int64(20000)
	res := billing.CheckFloorUnit(30000, 6000, &cost, 2500)
	if res.OK {
		t.Fatalf("G-06: 30,000 × 0.6 must be rejected, got %+v", res)
	}
	if res.ActualUnitMicro != 18000 || res.RequiredUnitMicro != 26667 || res.ShortfallMicro != 8667 {
		t.Fatalf("G-06: actual=%d required=%d shortfall=%d, want 18000/26667/8667",
			res.ActualUnitMicro, res.RequiredUnitMicro, res.ShortfallMicro)
	}
	if boundary := billing.CheckFloorUnit(26667, 10000, &cost, 2500); !boundary.OK {
		t.Fatalf("G-06: 26,667 must pass the exact 4/3 boundary")
	}
	if missing := billing.CheckFloorUnit(30000, 6000, nil, 2500); missing.OK || !missing.MissingCost {
		t.Fatalf("G-06: missing cost result=%+v, want OK=false + MissingCost", missing)
	}
	if internal := billing.CheckFloorUnit(30000, 0, &cost, 2500); !internal.OK || !internal.Skipped {
		t.Fatalf("G-06: internal ratio 0 must skip the floor: %+v", internal)
	}

	ops := newUser(t, userOpts{role: "OPS"})
	modelID := newModel(t, modelName(), "LISTED")
	// The implemented enforcement point is the price write: a sell price that
	// falls under the contract group's floor is refused with the shortfall.
	rec := call(t, ops, http.MethodPost, "/api/admin/prices", map[string]any{
		"model_id":              modelID,
		"in_price_micro_per_k":  30000,
		"out_price_micro_per_k": 60000,
		"cost_in_micro_per_k":   20000,
		"cost_out_micro_per_k":  20000,
		"effective_from":        time.Now().UTC().Format(time.RFC3339),
	})
	body := requireCode(t, "G-06 price floor", rec, http.StatusBadRequest, "invalid_request_body")
	if !strings.Contains(body.Error.Message, "差额") || !strings.Contains(body.Error.Message, "contract") {
		t.Fatalf("G-06: refusal does not name the group and the shortfall: %q", body.Error.Message)
	}
	// PATCH /api/admin/groups validates the value but does not run the floor;
	// the refusal lives on the price write instead. Recorded, not hidden.
	t.Logf("G-06 DEVIATION: the group endpoint has no margin-floor check; the floor is enforced on price create/update")
}

// G-07 — invoicing: real-name gate, payable ceiling, PENDING -> ISSUED.
func TestGWT_G07_InvoiceFlow(t *testing.T) {
	verified := newUser(t, userOpts{verified: true})
	orderID := newPaidOrderDB(t, verified, 50000)

	create := call(t, verified, http.MethodPost, "/api/console/invoices", map[string]any{
		"recharge_order_id": orderID,
		"title_type":        "PERSONAL",
		"title":             "t21 invoice",
		"receive_email":     "invoice@test.local",
		"amount_fen":        50000,
	})
	requireStatus(t, "G-07 create invoice", create, http.StatusCreated)
	var created struct {
		Invoice struct {
			ID        int64  `json:"id"`
			State     string `json:"state"`
			AmountFen int64  `json:"amount_fen"`
		} `json:"invoice"`
	}
	decodeBody(t, create, &created)
	if created.Invoice.AmountFen != 50000 {
		t.Fatalf("G-07: amount=%d, want 50,000 fen (the paid amount)", created.Invoice.AmountFen)
	}
	// PRD G-07 calls the initial state PENDING; the T13 enum uses APPLIED.
	if created.Invoice.State != "APPLIED" {
		t.Fatalf("G-07: initial state=%s, want APPLIED (PRD text says PENDING)", created.Invoice.State)
	}
	t.Logf("G-07 DEVIATION: PRD appendix D writes invoice state PENDING; t_invoice_apply uses APPLIED")

	overOrder := newPaidOrderDB(t, verified, 10000)
	over := call(t, verified, http.MethodPost, "/api/console/invoices", map[string]any{
		"recharge_order_id": overOrder,
		"title_type":        "PERSONAL",
		"title":             "t21 over",
		"receive_email":     "invoice@test.local",
		"amount_fen":        20000,
	})
	requireCode(t, "G-07 over amount", over, http.StatusBadRequest, "invalid_request_body")

	unverified := newUser(t, userOpts{})
	unverifiedOrder := newPaidOrderDB(t, unverified, 50000)
	gate := call(t, unverified, http.MethodPost, "/api/console/invoices", map[string]any{
		"recharge_order_id": unverifiedOrder,
		"title_type":        "PERSONAL",
		"title":             "t21 unverified",
		"receive_email":     "invoice@test.local",
	})
	requireCode(t, "G-07 unverified", gate, http.StatusForbidden, "account_not_verified")

	finance := newUser(t, userOpts{role: "FINANCE"})
	taskID := reviewTaskID(t, "INVOICE_APPLY", created.Invoice.ID)
	approve := call(t, finance, http.MethodPost, fmt.Sprintf("/api/admin/reviews/%d/approve", taskID),
		map[string]any{"invoice_url": "https://invoice.example/t21.pdf"})
	requireStatus(t, "G-07 approve", approve, http.StatusOK)

	var state string
	var issuedAt sql.NullTime
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT state, issued_at FROM t_invoice_apply WHERE id = ?`, created.Invoice.ID).
		Scan(&state, &issuedAt); err != nil {
		t.Fatalf("G-07: read invoice: %v", err)
	}
	if state != "ISSUED" || !issuedAt.Valid {
		t.Fatalf("G-07: state=%s issued_at=%v, want ISSUED/set", state, issuedAt.Time)
	}
}

// G-08 — a quota request does not change anything before approval; approval
// writes an OPS_OVERRIDE policy with a 30-day life.
func TestGWT_G08_QuotaApplyApproval(t *testing.T) {
	plan := planID(t, "STANDARD")
	user := newUser(t, userOpts{planID: &plan})
	finance := newUser(t, userOpts{role: "FINANCE"})

	apply := call(t, user, http.MethodPost, "/api/console/quota-applies", map[string]any{
		"target_quota_micro": int64(60_000_000),
		"apply_reason":       "G-08 capacity review",
	})
	requireStatus(t, "G-08 submit", apply, http.StatusCreated)
	var created struct {
		Apply struct {
			ID          int64  `json:"id"`
			State       string `json:"state"`
			CurrentQuot int64  `json:"current_quota_micro"`
		} `json:"apply"`
	}
	decodeBody(t, apply, &created)
	if created.Apply.State != "PENDING" {
		t.Fatalf("G-08: state=%s, want PENDING", created.Apply.State)
	}
	if created.Apply.CurrentQuot != 50000000 {
		t.Fatalf("G-08: current quota=%d, want 50,000,000 (STANDARD)", created.Apply.CurrentQuot)
	}
	if n := policyCount(t, user); n != 0 {
		t.Fatalf("G-08: %d quota policies exist before approval, want 0", n)
	}

	taskID := reviewTaskID(t, "QUOTA_APPLY", created.Apply.ID)
	approve := call(t, finance, http.MethodPost, fmt.Sprintf("/api/admin/reviews/%d/approve", taskID),
		map[string]any{"reason": "G-08 approved"})
	requireStatus(t, "G-08 approve", approve, http.StatusOK)

	var state string
	var approved int64
	var from, to time.Time
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT state, approved_quota_micro, effective_from, effective_to
		  FROM t_quota_apply WHERE id = ?`, created.Apply.ID).
		Scan(&state, &approved, &from, &to); err != nil {
		t.Fatalf("G-08: read apply: %v", err)
	}
	if state != "APPROVED" || approved != 60000000 {
		t.Fatalf("G-08: state=%s approved=%d, want APPROVED/60,000,000", state, approved)
	}
	cst := time.FixedZone("CST", 8*3600)
	local := time.Now().In(cst)
	wantFrom := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, cst).AddDate(0, 0, 1)
	if d := from.In(cst).Sub(wantFrom); d < -time.Minute || d > time.Minute {
		t.Fatalf("G-08: effective_from=%s, want next CST midnight %s", from.In(cst), wantFrom)
	}
	if d := to.Sub(from).Round(time.Hour); d != 30*24*time.Hour {
		t.Fatalf("G-08: effective window=%v, want 30 days", d)
	}

	var source string
	var daily int64
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT source, COALESCE(daily_quota_micro, 0) FROM t_rate_limit_policy
		 WHERE scope_type = 'USER' AND scope_id = ?
		 ORDER BY id DESC LIMIT 1`, user).Scan(&source, &daily); err != nil {
		t.Fatalf("G-08: read policy: %v", err)
	}
	if source != "QUOTA_APPLY" || daily != 60000000 {
		t.Fatalf("G-08: policy=%s/%d, want QUOTA_APPLY/60,000,000", source, daily)
	}

	// Rejection keeps the quota unchanged.
	rejectApply := call(t, user, http.MethodPost, "/api/console/quota-applies", map[string]any{
		"target_quota_micro": int64(70_000_000),
		"apply_reason":       "G-08 rejection path",
	})
	requireStatus(t, "G-08 second submit", rejectApply, http.StatusCreated)
	var second struct {
		Apply struct {
			ID int64 `json:"id"`
		} `json:"apply"`
	}
	decodeBody(t, rejectApply, &second)
	rejectTask := reviewTaskID(t, "QUOTA_APPLY", second.Apply.ID)
	reject := call(t, finance, http.MethodPost, fmt.Sprintf("/api/admin/reviews/%d/reject", rejectTask),
		map[string]any{"reason": "G-08 insufficient evidence"})
	requireStatus(t, "G-08 reject", reject, http.StatusOK)
	var rejectedState string
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT state FROM t_quota_apply WHERE id = ?`, second.Apply.ID).Scan(&rejectedState); err != nil {
		t.Fatalf("G-08: read rejected apply: %v", err)
	}
	if rejectedState != "REJECTED" {
		t.Fatalf("G-08: rejected state=%s, want REJECTED", rejectedState)
	}
	if n := policyCount(t, user); n != 1 {
		t.Fatalf("G-08: policies after rejection=%d, want 1 (the approved one)", n)
	}
}

// G-09 — the trial is granted once per device, phone, IP segment and account,
// and the grant is 7,000 credits valid for 90 days.
func TestGWT_G09_TrialAntiAbuseAndGrant(t *testing.T) {
	user := newUser(t, userOpts{})
	fp := fmt.Sprintf("fp-%d-%d", time.Now().UnixNano(), nextInt())
	// Use a fresh /24 per run: the IP-segment check reads the last 30 days of
	// TRIAL_CLAIM audit rows, so a repeated segment from an earlier run would
	// block a legitimate first claim.
	n := time.Now().UnixNano()
	remote := fmt.Sprintf("100.%d.%d.1:1234", n&0xff, (n>>8)&0xff)

	claim := call(t, user, http.MethodPost, "/api/console/trial/claim",
		map[string]any{"fingerprint": fp}, callOpts{remoteAddr: remote})
	requireStatus(t, "G-09 claim", claim, http.StatusOK)
	var body struct {
		PlanCode       string `json:"plan_code"`
		GrantedMicro   int64  `json:"granted_micro"`
		GrantedCredits int64  `json:"granted_credits"`
		TrialExpireAt  string `json:"trial_expire_at"`
		BonusExpireAt  string `json:"bonus_expire_at"`
		BalanceAfter   int64  `json:"balance_after_micro"`
		Checks         string `json:"anti_abuse_checks"`
	}
	decodeBody(t, claim, &body)
	if body.PlanCode != "TRIAL" || body.GrantedMicro != 70000000 || body.GrantedCredits != 7000 {
		t.Fatalf("G-09: grant=%s/%d/%d, want TRIAL/70,000,000/7,000", body.PlanCode, body.GrantedMicro, body.GrantedCredits)
	}
	if body.BalanceAfter != 70000000 {
		t.Fatalf("G-09: balance after=%d, want 70,000,000", body.BalanceAfter)
	}
	for _, factor := range []string{"DEVICE", "PHONE", "IP_SEGMENT", "ACCOUNT"} {
		if !strings.Contains(body.Checks, factor) {
			t.Fatalf("G-09: anti-abuse checks %q omit %s", body.Checks, factor)
		}
	}
	trialExpire := mustParseRFC3339(t, body.TrialExpireAt)
	if d := time.Until(trialExpire); d < 6*24*time.Hour || d > 8*24*time.Hour {
		t.Fatalf("G-09: trial expiry in %v, want 7 days", d)
	}
	bonusExpire := mustParseRFC3339(t, body.BonusExpireAt)
	if d := time.Until(bonusExpire); d < 89*24*time.Hour || d > 91*24*time.Hour {
		t.Fatalf("G-09: bonus expiry in %v, want 90 days", d)
	}

	var planCode string
	var daily int64
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT p.code, COALESCE(p.daily_quota_micro, 0)
		  FROM t_user u JOIN t_plan p ON p.id = u.plan_id WHERE u.id = ?`, user).
		Scan(&planCode, &daily); err != nil {
		t.Fatalf("G-09: read trial plan: %v", err)
	}
	if planCode != "TRIAL" || daily != 10000000 {
		t.Fatalf("G-09: plan=%s daily=%d, want TRIAL/10,000,000 (1,000 credits)", planCode, daily)
	}
	if 7*(daily/10000) != body.GrantedCredits {
		t.Fatalf("G-09: 7 × %d credits != granted %d", daily/10000, body.GrantedCredits)
	}

	var granted, seen int64
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT trial_granted, seen_count FROM t_device_fingerprint
		 WHERE fp_hash = ?`, fpHash(fingerprint, fp)).
		Scan(&granted, &seen); err != nil {
		t.Fatalf("G-09: read fingerprint: %v", err)
	}
	if granted != 1 {
		t.Fatalf("G-09: device trial_granted=%d, want 1", granted)
	}

	again := call(t, user, http.MethodPost, "/api/console/trial/claim",
		map[string]any{"fingerprint": fp + "-again"}, callOpts{remoteAddr: remote})
	requireCode(t, "G-09 same account", again, http.StatusConflict, "trial_already_claimed")

	other := newUser(t, userOpts{})
	sameDevice := call(t, other, http.MethodPost, "/api/console/trial/claim",
		map[string]any{"fingerprint": fp}, callOpts{remoteAddr: fmt.Sprintf("101.%d.%d.1:1234", n&0xff, (n>>8)&0xff)})
	requireCode(t, "G-09 same device", sameDevice, http.StatusConflict, "trial_already_claimed")

	// PRD G-09 requires a 24-hour issuance delay and names the missing gate in
	// the refusal. V1.0 implements neither; the shared refusal deliberately
	// hides which factor matched. Both are recorded, not papered over.
	t.Logf("G-09 DEVIATION: the 24h issuance delay is not implemented, and the refusal hides the matched factor by design")
}

// G-10 — the 429 contract: body fields, recovery instant and five headers.
func TestGWT_G10_429Contract(t *testing.T) {
	limiter := ratelimit.New(testRedis, redisx.NewClock(nil, nil), nil, nil, nil)
	now := time.Now()
	nowMS := now.UnixMilli()
	limits := ratelimit.Limits{
		RPM: 60, TPM: 1_000_000_000, Concurrency: 1000,
		DailyQuotaMicro: 1_000_000_000, DailyEnabled: true,
	}
	keyID := int64(9_100_000 + nextInt()%100000)
	userID := keyID + 1

	var last ratelimit.Verdict
	for i := 0; i < 60; i++ {
		v, err := limiter.Acquire(context.Background(), ratelimit.AcquireInput{
			KeyID: keyID, UserID: userID, NowMS: nowMS, RequestID: rid(),
			EstTokens: 1, Limits: limits,
		})
		if err != nil {
			t.Fatalf("G-10: acquire %d: %v", i, err)
		}
		if !v.Allowed {
			t.Fatalf("G-10: request %d denied before the 60-request ceiling", i+1)
		}
		last = v
	}
	_ = last
	denied, err := limiter.Acquire(context.Background(), ratelimit.AcquireInput{
		KeyID: keyID, UserID: userID, NowMS: nowMS, RequestID: rid(),
		EstTokens: 1, Limits: limits,
	})
	if err != nil {
		t.Fatalf("G-10: 61st acquire: %v", err)
	}
	if denied.Allowed || denied.OutwardCode() != ratelimit.CodeRPM {
		t.Fatalf("G-10: 61st verdict=%+v, want rpm_limit_exceeded", denied)
	}
	apiErr := apierr.FromVerdictAt(denied, now).WithRequestID(rid())
	rec := httptest.NewRecorder()
	if err := apierr.Write(rec, apiErr); err != nil {
		t.Fatalf("G-10: write: %v", err)
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("G-10: status=%d, want 429", rec.Code)
	}
	var body apiErrorBody
	decodeBody(t, rec, &body)
	if body.Error.Type != "rate_limit_exceeded" || body.Error.Code != "rpm_limit_exceeded" {
		t.Fatalf("G-10: body type/code=%s/%s, want rate_limit_exceeded/rpm_limit_exceeded",
			body.Error.Type, body.Error.Code)
	}
	if body.Error.RequestID == "" || body.Error.Hint == "" {
		t.Fatalf("G-10: request_id/hint missing: %+v", body.Error)
	}
	if body.Error.Limit == nil || *body.Error.Limit != 60 {
		t.Fatalf("G-10: limit=%v, want 60", body.Error.Limit)
	}
	if body.Error.Remaining == nil || *body.Error.Remaining != 0 {
		t.Fatalf("G-10: remaining=%v, want 0", body.Error.Remaining)
	}
	resetAt, err := time.Parse(time.RFC3339, body.Error.ResetAt)
	if err != nil {
		t.Fatalf("G-10: reset_at %q: %v", body.Error.ResetAt, err)
	}
	if resetAt.Unix()%60 != 0 {
		t.Fatalf("G-10: reset_at %s is not a whole minute", resetAt)
	}
	retry, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || retry < 1 || retry > 60 {
		t.Fatalf("G-10: Retry-After=%q, want 1..60", rec.Header().Get("Retry-After"))
	}
	for _, h := range []string{
		"X-RateLimit-Limit-Requests", "X-RateLimit-Remaining-Requests", "X-RateLimit-Reset-Requests",
	} {
		if rec.Header().Get(h) == "" {
			t.Fatalf("G-10: missing header %s", h)
		}
	}

	// Four dimensions hit at once: the outward code is the latest recovery.
	all := ratelimit.Verdict{
		Allowed: false, Code: ratelimit.CodeConcurrency, Limit: 10, Total: 10, Remaining: 0,
		ResetAt: ratelimit.NextResetUnix(now),
		AllHit:  []string{ratelimit.CodeConcurrency, ratelimit.CodeTPM, ratelimit.CodeRPM, ratelimit.CodeDailyQuota},
	}
	multi := apierr.FromVerdictAt(all, now)
	if !strings.Contains(multi.Code, "daily_quota_exceeded") {
		t.Fatalf("G-10: multi-dim code=%s, want daily_quota_exceeded", multi.Code)
	}
	if !strings.Contains(multi.Message, "同时触顶") {
		t.Fatalf("G-10: multi-dim message does not list the other dimensions: %q", multi.Message)
	}

	// Normal response headers share one source.
	consumed := int64(5400)
	remaining := float64(994600)
	ok := httptest.NewRecorder()
	apierr.SetResponseHeaders(ok, apierr.ResponseMeta{
		RequestID: "rid-g10", CreditsConsumed: &consumed, CreditsRemaining: &remaining,
	})
	if ok.Header().Get("X-Request-Id") != "rid-g10" ||
		ok.Header().Get("X-Credits-Consumed") != "5400" ||
		ok.Header().Get("X-Credits-Remaining") != "994600" {
		t.Fatalf("G-10: normal headers = %v", ok.Header())
	}
}

// G-11 — the daily quota is account-scoped: exhausting one key blocks the
// other, and the next CST day starts from zero.
func TestGWT_G11_DailyQuotaIsAccountScoped(t *testing.T) {
	limiter := ratelimit.New(testRedis, redisx.NewClock(nil, nil), nil, nil, nil)
	now := time.Now()
	keyA := int64(9_200_000 + nextInt()%100000)
	keyB := keyA + 1
	userID := keyA + 2
	limits := ratelimit.Limits{
		RPM: 1000, TPM: 1_000_000_000, Concurrency: 1000,
		DailyQuotaMicro: 10_000_000, DailyEnabled: true,
	}
	acquire := func(keyID int64, micro int64, at time.Time) ratelimit.Verdict {
		t.Helper()
		v, err := limiter.Acquire(context.Background(), ratelimit.AcquireInput{
			KeyID: keyID, UserID: userID, NowMS: at.UnixMilli(),
			Day: ratelimit.Day(at), RequestID: rid(), EstMicro: micro, Limits: limits,
		})
		if err != nil {
			t.Fatalf("G-11: acquire key=%d: %v", keyID, err)
		}
		return v
	}

	// The Given starts with both keys having consumed 600 credits. Seed the
	// account counter to 1,200 credits (12,000,000 micro): a denied acquire
	// does not add to the counter, so replaying two allowed requests would
	// represent only 600 credits, not the given state.
	seedCtx := context.Background()
	if err := testRedis.Raw().Set(seedCtx, redisx.DailyQuotaKey(userID, ratelimit.Day(now)),
		"12000000", 48*time.Hour).Err(); err != nil {
		t.Fatalf("G-11: seed daily counter: %v", err)
	}

	b := acquire(keyB, 0, now)
	if b.Allowed || b.OutwardCode() != ratelimit.CodeDailyQuota {
		t.Fatalf("G-11: key B verdict=%+v, want daily_quota_exceeded (1,200 > 1,000 credits)", b)
	}
	aAgain := acquire(keyA, 0, now)
	if aAgain.Allowed || aAgain.OutwardCode() != ratelimit.CodeDailyQuota {
		t.Fatalf("G-11: key A was not blocked after the account quota was exhausted: %+v", aAgain)
	}

	nextDay := now.Add(24 * time.Hour)
	if v := acquire(keyA, 6_000_000, nextDay); !v.Allowed {
		t.Fatalf("G-11: the next CST day did not start from zero: %+v", v)
	}
	// Key-level daily quota was removed (D-5). The AcquireInput/Limits types
	// have exactly one DailyQuotaMicro field, owned by the USER dimension; a
	// key-scoped quota has no representation to configure.
	t.Logf("G-11: daily quota is account-scoped; AcquireInput carries no key-level daily field")
}

// G-12 — the console catalog and the gateway price cache show the same
// current price, and a CATALOG_ONLY model is absent from the catalog.
func TestGWT_G12_PriceConsistency(t *testing.T) {
	ops := newUser(t, userOpts{role: "OPS"})
	user := newUser(t, userOpts{})
	name := modelName()
	modelID := newModel(t, name, "LISTED")
	priceID := createPriceAPI(t, ops, modelID, 10000, 20000, 5000)
	publishPrices(t)

	if got := catalogPrice(t, user, name); got != 10000 {
		t.Fatalf("G-12: console catalog price=%d, want 10000", got)
	}

	lower := call(t, ops, http.MethodPatch, fmt.Sprintf("/api/admin/prices/%d", priceID), map[string]any{
		"in_price_micro_per_k": 9000,
		"effective_from":       time.Now().UTC().Format(time.RFC3339),
	})
	requireStatus(t, "G-12 update", lower, http.StatusOK)
	publishPrices(t)

	catalog := catalogPrice(t, user, name)
	raw, err := (&config.PriceCache{Redis: config.NewGoRedis(testRedis.Raw())}).GetStandard(context.Background(), modelID)
	if err != nil {
		t.Fatalf("G-12: read K19: %v", err)
	}
	rule, err := gw.ParsePrice(raw)
	if err != nil {
		t.Fatalf("G-12: parse K19: %v", err)
	}
	if catalog != 9000 || rule.InPriceMicroPerK != 9000 {
		t.Fatalf("G-12: catalog=%d K19=%d, want both 9000", catalog, rule.InPriceMicroPerK)
	}
	if rule.ID != priceID {
		t.Fatalf("G-12: K19 rule id=%d, want the current row %d", rule.ID, priceID)
	}

	// A CATALOG_ONLY model is not in the console catalog.
	hiddenName := modelName() + "-catalog-only"
	hiddenID := newModel(t, hiddenName, "CATALOG_ONLY")
	createPriceAPI(t, ops, hiddenID, 10000, 20000, 5000)
	publishPrices(t)
	if got := catalogPrice(t, user, hiddenName); got != -1 {
		t.Fatalf("G-12: CATALOG_ONLY model appeared in the catalog with price %d", got)
	}
	// The website /pricing page and the public price endpoint are front-end
	// and assumed-contract surfaces; the DOM-level comparison is Playwright
	// work (T21 second segment).
	t.Logf("G-12: console catalog and K19 agree; /pricing DOM verification is deferred to the Playwright segment")
}

// ---------------------------------------------------------------------------
// GWT fixtures and lookups
// ---------------------------------------------------------------------------

func newRechargeOrderDB(t *testing.T, uid, amountMicro int64) int64 {
	t.Helper()
	now := time.Now().UTC()
	res, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_recharge_order
			(order_no, user_id, order_type, pay_amount_fen, recharge_micro, bonus_micro,
			 pay_channel, state, refundable_micro, expire_at, created_at, updated_at)
		VALUES (?,?, 'CUSTOM', ?,?,0,'WECHAT','PAID',0,?,?,?)`,
		fmt.Sprintf("T21O%d%06d", time.Now().Unix()%1e7, nextInt()%1000000),
		uid, amountMicro/10000, amountMicro, now.Add(time.Hour), now, now)
	if err != nil {
		t.Fatalf("insert recharge order: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("recharge order id: %v", err)
	}
	return id
}

func newPaidOrderDB(t *testing.T, uid, payFen int64) int64 {
	t.Helper()
	now := time.Now().UTC()
	res, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_recharge_order
			(order_no, user_id, order_type, pay_amount_fen, recharge_micro, bonus_micro,
			 pay_channel, pay_txn_id, state, paid_at, refundable_micro, expire_at, created_at, updated_at)
		VALUES (?,?, 'CUSTOM', ?,?,0,'WECHAT',?,'PAID',?,0,?,?,?)`,
		fmt.Sprintf("T21P%d%06d", time.Now().Unix()%1e7, nextInt()%1000000),
		uid, payFen, payFen*10000, fmt.Sprintf("TXN%d", time.Now().UnixNano()),
		now, now.Add(time.Hour), now, now)
	if err != nil {
		t.Fatalf("insert paid order: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("paid order id: %v", err)
	}
	return id
}

func newRefundOrderDB(t *testing.T, uid, rechargeOrderID int64) int64 {
	t.Helper()
	now := time.Now().UTC()
	res, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_refund_order
			(refund_no, user_id, recharge_order_id, apply_micro, refund_micro,
			 refund_fen, fee_fen, state, created_at, updated_at)
		VALUES (?,?,?,0,0,0,0,'APPLIED',?,?)`,
		fmt.Sprintf("T21R%d%06d", time.Now().Unix()%1e7, nextInt()%1000000),
		uid, rechargeOrderID, now, now)
	if err != nil {
		t.Fatalf("insert refund order: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("refund order id: %v", err)
	}
	return id
}

func newInvoiceDB(t *testing.T, uid, rechargeOrderID int64, state string) int64 {
	t.Helper()
	now := time.Now().UTC()
	res, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_invoice_apply
			(apply_no, user_id, recharge_order_id, title_type, title,
			 receive_email, amount_fen, state, created_at, updated_at)
		VALUES (?,?,?,'PERSONAL','t21','t21@test.local',100,?,?,?)`,
		fmt.Sprintf("T21I%d%06d", time.Now().Unix()%1e7, nextInt()%1000000),
		uid, rechargeOrderID, state, now, now)
	if err != nil {
		t.Fatalf("insert invoice: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("invoice id: %v", err)
	}
	return id
}

func reviewTaskID(t *testing.T, bizType string, bizID int64) int64 {
	t.Helper()
	var id int64
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT id FROM t_review_task
		 WHERE biz_type = ? AND biz_id = ? AND state = 'PENDING'
		 ORDER BY id DESC LIMIT 1`, bizType, bizID).Scan(&id); err != nil {
		t.Fatalf("review task %s/%d: %v", bizType, bizID, err)
	}
	return id
}

func policyCount(t *testing.T, uid int64) int {
	t.Helper()
	var n int
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_rate_limit_policy WHERE scope_type = 'USER' AND scope_id = ?`,
		uid).Scan(&n); err != nil {
		t.Fatalf("count policies: %v", err)
	}
	return n
}

func mustParseRFC3339(t *testing.T, raw string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return v
}

func fpHash(key, raw string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(raw))
	return hex.EncodeToString(mac.Sum(nil))
}

type catalogPricing struct {
	InPriceMicroPerK int64 `json:"in_price_micro_per_k"`
}

type catalogItem struct {
	ModelName string          `json:"model_name"`
	Pricing   *catalogPricing `json:"pricing"`
}

func catalogPrice(t *testing.T, uid int64, modelName string) int64 {
	t.Helper()
	rec := call(t, uid, http.MethodGet, "/api/console/models?page_size=100&q="+modelName, nil)
	requireStatus(t, "catalog list", rec, http.StatusOK)
	var body struct {
		Items []catalogItem `json:"items"`
	}
	decodeBody(t, rec, &body)
	for _, item := range body.Items {
		if item.ModelName == modelName && item.Pricing != nil {
			return item.Pricing.InPriceMicroPerK
		}
	}
	return -1
}
