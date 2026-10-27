package fund

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"yunshu.dev/core/internal/ledger"
	"yunshu.dev/core/internal/redisx"
)

// A2 / G-03: ten concurrent deliveries of the same channel transaction credit
// the account exactly once — one paying callback, two batches (recharge +
// bonus), two ledger entries (one per batch, PRD G-03 Then.3), and the
// remaining nine answer 200 without writing anything.
func TestCallbackIsIdempotentUnderConcurrency(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	plan := planByCode(t, "STANDARD")

	order, _ := createOrder(t, svc, uid, map[string]any{
		"order_type":  OrderTypePlan,
		"plan_id":     plan.ID,
		"pay_channel": PayChannelWechat,
	})
	if order.State != OrderStatePending {
		t.Fatalf("new order state = %q, want PENDING", order.State)
	}
	// hard constraint 2: nothing is credited before the payment succeeds.
	if b := balanceOf(t, uid); b.Total != 0 {
		t.Fatalf("balance before payment = %d, want 0", b.Total)
	}

	const deliveries = 10
	tradeNo := "WX" + fmt.Sprint(time.Now().UnixNano())
	var wg sync.WaitGroup
	statuses := make([]int, deliveries)
	bodies := make([]string, deliveries)
	for i := 0; i < deliveries; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := successfulCallback(t, svc, order, tradeNo)
			statuses[i] = rec.Code
			bodies[i] = rec.Body.String()
		}(i)
	}
	wg.Wait()

	for i, code := range statuses {
		if code != http.StatusOK {
			t.Fatalf("delivery %d status = %d body=%s", i, code, bodies[i])
		}
	}
	b := balanceOf(t, uid)
	if b.Recharge != plan.GrantRechargeMicro || b.Bonus != plan.GrantBonusMicro {
		t.Fatalf("balance = recharge %d / bonus %d, want %d / %d",
			b.Recharge, b.Bonus, plan.GrantRechargeMicro, plan.GrantBonusMicro)
	}
	if got := countBatches(t, uid); got != 2 {
		t.Fatalf("credit batches = %d, want 2 (recharge + bonus, never merged)", got)
	}
	if got := countLedger(t, uid); got != 2 {
		t.Fatalf("ledger entries = %d, want 2 (one per batch, PRD G-03 Then.3)", got)
	}
	if st := orderState(t, order.ID); st != OrderStatePaid {
		t.Fatalf("order state = %q, want PAID", st)
	}
	t.Logf("deliveries=%d statuses=%v credit=%d/%d batches=%d entries=%d",
		deliveries, statuses, b.Recharge, b.Bonus, countBatches(t, uid), countLedger(t, uid))
}

// G-03 分账: the recharge batch never expires and is refundable; the bonus
// batch expires billing.bonus_expire_days days after payment and is not.
func TestSplitBatchDetails(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	plan := planByCode(t, "STANDARD")
	order, _ := createOrder(t, svc, uid, map[string]any{
		"order_type": OrderTypePlan, "plan_id": plan.ID, "pay_channel": PayChannelAlipay,
	})
	paidAt := time.Now().UTC()
	rec := successfulCallback(t, svc, order, "ALI"+fmt.Sprint(paidAt.UnixNano()))
	if rec.Code != http.StatusOK {
		t.Fatalf("callback status = %d body=%s", rec.Code, rec.Body.String())
	}

	rows, err := testDB.QueryContext(context.Background(), `
		SELECT batch_type, face_value_micro, refundable, expire_at, create_ledger_id
		  FROM t_credit_batch WHERE user_id = ? ORDER BY id`, uid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type batch struct {
		kind    string
		face    int64
		refund  int64
		expire  *time.Time
		entryID *int64
	}
	var got []batch
	for rows.Next() {
		var (
			x      batch
			expire sql.NullTime
			entry  sql.NullInt64
		)
		if err := rows.Scan(&x.kind, &x.face, &x.refund, &expire, &entry); err != nil {
			t.Fatal(err)
		}
		if expire.Valid {
			v := expire.Time.UTC()
			x.expire = &v
		}
		if entry.Valid {
			v := entry.Int64
			x.entryID = &v
		}
		got = append(got, x)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("batches = %d, want 2", len(got))
	}
	if got[0].kind != "RECHARGE" || got[0].refund != 1 || got[0].expire != nil {
		t.Fatalf("recharge batch = %+v, want RECHARGE/refundable/no expiry", got[0])
	}
	if got[1].kind != "BONUS" || got[1].refund != 0 || got[1].expire == nil {
		t.Fatalf("bonus batch = %+v, want BONUS/non-refundable/expiry", got[1])
	}
	days := svc.bonusExpireDays(context.Background(), plan.BonusValidDays)
	wantExpire := paidAt.Add(time.Duration(days) * 24 * time.Hour)
	diff := got[1].expire.Sub(wantExpire)
	if diff < -time.Minute || diff > time.Minute {
		t.Fatalf("bonus expire_at = %s, want ~%s (%d days from billing.bonus_expire_days)",
			got[1].expire, wantExpire, days)
	}
	if got[0].entryID == nil || got[1].entryID == nil || *got[0].entryID == *got[1].entryID {
		t.Fatalf("each batch must carry its own ledger entry: %v / %v", got[0].entryID, got[1].entryID)
	}
	t.Logf("recharge batch: face=%d refundable=%d expire=nil entry=%d", got[0].face, got[0].refund, *got[0].entryID)
	t.Logf("bonus batch:    face=%d refundable=%d expire=%s entry=%d (%d days from config)",
		got[1].face, got[1].refund, got[1].expire.Format(time.RFC3339), *got[1].entryID, days)
}

// A3: a PENDING order cannot be credited by a callback that does not match the
// order's own amount, and no credit path exists before the state CAS.
func TestPendingOrderCannotBeCredited(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	plan := planByCode(t, "STANDARD")
	order, _ := createOrder(t, svc, uid, map[string]any{
		"order_type": OrderTypePlan, "plan_id": plan.ID, "pay_channel": PayChannelWechat,
	})

	// A forged amount is rejected before any write.
	rec := callCallback(t, svc, "wechat", map[string]any{
		"out_trade_no": order.OrderNo,
		"trade_no":     "WXFORGED",
		"amount_fen":   order.PayAmountFen - 1,
		"status":       callbackStatusSuccess,
		"sign":         signCallback(testPaySecret, order.OrderNo, "WXFORGED", order.PayAmountFen-1, callbackStatusSuccess),
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("forged amount status = %d body=%s", rec.Code, rec.Body.String())
	}
	if body := errorBody(t, rec); body.Error.Code != "invalid_request_body" {
		t.Fatalf("forged amount code = %q", body.Error.Code)
	}
	if b := balanceOf(t, uid); b.Total != 0 {
		t.Fatalf("balance = %d after rejected callback, want 0", b.Total)
	}
	if got := countLedger(t, uid); got != 0 {
		t.Fatalf("ledger entries = %d, want 0", got)
	}
	if st := orderState(t, order.ID); st != OrderStatePending {
		t.Fatalf("order state = %q, want PENDING", st)
	}
}

// G-03 And: a bad signature credits nothing and leaves the order PENDING.
func TestCallbackSignatureRejected(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	plan := planByCode(t, "STANDARD")
	order, _ := createOrder(t, svc, uid, map[string]any{
		"order_type": OrderTypePlan, "plan_id": plan.ID, "pay_channel": PayChannelWechat,
	})

	rec := callCallback(t, svc, "wechat", map[string]any{
		"out_trade_no": order.OrderNo,
		"trade_no":     "WXSIG",
		"amount_fen":   order.PayAmountFen,
		"status":       callbackStatusSuccess,
		"sign":         "deadbeef",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad signature status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := errorBody(t, rec)
	if body.Error.Code != "invalid_request_body" || !strings.Contains(body.Error.Message, "sign") {
		t.Fatalf("bad signature body = %+v, want invalid_request_body naming sign", body.Error)
	}
	if b := balanceOf(t, uid); b.Total != 0 {
		t.Fatalf("balance = %d, want 0", b.Total)
	}
	if st := orderState(t, order.ID); st != OrderStatePending {
		t.Fatalf("order state = %q, want PENDING", st)
	}
}

// A missing signing secret disables the channel with a 503; it must never
// silently skip verification.
func TestCallbackWithoutSecretIsUnavailable(t *testing.T) {
	svc, _ := newTestService(t)
	svc.PaySecrets = map[string]string{}
	uid := newTestUser(t, nil)
	plan := planByCode(t, "STANDARD")
	order, _ := createOrder(t, svc, uid, map[string]any{
		"order_type": OrderTypePlan, "plan_id": plan.ID, "pay_channel": PayChannelWechat,
	})
	rec := callCallback(t, svc, "wechat", map[string]any{
		"out_trade_no": order.OrderNo,
		"trade_no":     "WXNOSECRET",
		"amount_fen":   order.PayAmountFen,
		"status":       callbackStatusSuccess,
		"sign":         signCallback(testPaySecret, order.OrderNo, "WXNOSECRET", order.PayAmountFen, callbackStatusSuccess),
	})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	if b := balanceOf(t, uid); b.Total != 0 {
		t.Fatalf("balance = %d, want 0", b.Total)
	}
}

// A8: buying a higher tier replaces the four dimensions immediately, and the
// older tier's bonus batch keeps its original expiry.
func TestPlanUpgradeOverridesSpecs(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	standard := planByCode(t, "STANDARD")
	pro := planByCode(t, "PRO")

	o1, _ := createOrder(t, svc, uid, map[string]any{
		"order_type": OrderTypePlan, "plan_id": standard.ID, "pay_channel": PayChannelWechat,
	})
	if rec := successfulCallback(t, svc, o1, "WXUP1"); rec.Code != http.StatusOK {
		t.Fatalf("first callback status = %d body=%s", rec.Code, rec.Body.String())
	}
	source, rpm, tpm, conc, daily := activePolicy(t, uid)
	if source != "PLAN" || rpm != standard.RPM || tpm != standard.TPM || conc != standard.Concurrency {
		t.Fatalf("after STANDARD: source=%s rpm=%d tpm=%d conc=%d, want PLAN/%d/%d/%d",
			source, rpm, tpm, conc, standard.RPM, standard.TPM, standard.Concurrency)
	}
	if daily == nil || *daily != *standard.DailyQuotaMicro {
		t.Fatalf("after STANDARD: daily=%v, want %d", daily, *standard.DailyQuotaMicro)
	}

	var standardBonusExpire time.Time
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT expire_at FROM t_credit_batch
		 WHERE user_id = ? AND batch_type = 'BONUS' ORDER BY id LIMIT 1`, uid).
		Scan(&standardBonusExpire); err != nil {
		t.Fatal(err)
	}

	o2, _ := createOrder(t, svc, uid, map[string]any{
		"order_type": OrderTypePlan, "plan_id": pro.ID, "pay_channel": PayChannelWechat,
	})
	if rec := successfulCallback(t, svc, o2, "WXUP2"); rec.Code != http.StatusOK {
		t.Fatalf("second callback status = %d body=%s", rec.Code, rec.Body.String())
	}
	source, rpm, tpm, conc, daily = activePolicy(t, uid)
	if source != "PLAN" || rpm != pro.RPM || tpm != pro.TPM || conc != pro.Concurrency {
		t.Fatalf("after PRO: source=%s rpm=%d tpm=%d conc=%d, want PLAN/%d/%d/%d",
			source, rpm, tpm, conc, pro.RPM, pro.TPM, pro.Concurrency)
	}
	if daily == nil || *daily != *pro.DailyQuotaMicro {
		t.Fatalf("after PRO: daily=%v, want %d", daily, *pro.DailyQuotaMicro)
	}
	var standardBonusExpireAfter time.Time
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT expire_at FROM t_credit_batch
		 WHERE user_id = ? AND batch_type = 'BONUS' ORDER BY id LIMIT 1`, uid).
		Scan(&standardBonusExpireAfter); err != nil {
		t.Fatal(err)
	}
	if !standardBonusExpireAfter.Equal(standardBonusExpire) {
		t.Fatalf("STANDARD bonus expiry moved: %s -> %s (upgrade must not extend it)",
			standardBonusExpire, standardBonusExpireAfter)
	}
	b := balanceOf(t, uid)
	if b.Recharge != standard.GrantRechargeMicro+pro.GrantRechargeMicro {
		t.Fatalf("recharge balance = %d, want %d (accumulated, not replaced)",
			b.Recharge, standard.GrantRechargeMicro+pro.GrantRechargeMicro)
	}
	t.Logf("STANDARD→PRO: rpm %d→%d, tpm %d→%d, conc %d→%d, bonus expiry kept %s",
		standard.RPM, rpm, standard.TPM, tpm, standard.Concurrency, conc,
		standardBonusExpire.Format(time.RFC3339))
}

// A9: buying the same tier twice keeps the specs and accumulates credits in
// separate batches, each with its own expiry.
func TestRepeatPurchaseSameTier(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	standard := planByCode(t, "STANDARD")

	for i := 0; i < 2; i++ {
		order, _ := createOrder(t, svc, uid, map[string]any{
			"order_type": OrderTypePlan, "plan_id": standard.ID, "pay_channel": PayChannelWechat,
		})
		rec := successfulCallback(t, svc, order, fmt.Sprintf("WXSAME%d", i))
		if rec.Code != http.StatusOK {
			t.Fatalf("callback %d status = %d body=%s", i, rec.Code, rec.Body.String())
		}
	}
	_, rpm, tpm, conc, _ := activePolicy(t, uid)
	if rpm != standard.RPM || tpm != standard.TPM || conc != standard.Concurrency {
		t.Fatalf("specs = %d/%d/%d, want unchanged %d/%d/%d",
			rpm, tpm, conc, standard.RPM, standard.TPM, standard.Concurrency)
	}
	var bonusBatches, bonusWithExpiry int
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT COUNT(*), COUNT(expire_at) FROM t_credit_batch
		 WHERE user_id = ? AND batch_type = 'BONUS'`, uid).
		Scan(&bonusBatches, &bonusWithExpiry); err != nil {
		t.Fatal(err)
	}
	if bonusBatches != 2 || bonusWithExpiry != 2 {
		t.Fatalf("bonus batches = %d (with expiry %d), want 2/2", bonusBatches, bonusWithExpiry)
	}
	b := balanceOf(t, uid)
	if b.Bonus != 2*standard.GrantBonusMicro {
		t.Fatalf("bonus balance = %d, want %d", b.Bonus, 2*standard.GrantBonusMicro)
	}
	t.Logf("repeat purchase: rpm=%d tpm=%d conc=%d bonus_batches=%d bonus_micro=%d",
		rpm, tpm, conc, bonusBatches, b.Bonus)
}

// A11: the status filter and the order-number search intersect.
func TestOrderListFilterIntersects(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	plan := planByCode(t, "STANDARD")

	paid, _ := createOrder(t, svc, uid, map[string]any{
		"order_type": OrderTypePlan, "plan_id": plan.ID, "pay_channel": PayChannelWechat,
	})
	if rec := successfulCallback(t, svc, paid, "WXLIST1"); rec.Code != http.StatusOK {
		t.Fatalf("callback status = %d", rec.Code)
	}
	pending, _ := createOrder(t, svc, uid, map[string]any{
		"order_type": OrderTypeCustom, "pay_amount_fen": 10000, "pay_channel": PayChannelAlipay,
	})
	closed, _ := createOrder(t, svc, uid, map[string]any{
		"order_type": OrderTypeCustom, "pay_amount_fen": 20000, "pay_channel": PayChannelAlipay,
	})
	if _, err := testDB.ExecContext(context.Background(),
		`UPDATE t_recharge_order SET state = 'CLOSED' WHERE id = ?`, closed.ID); err != nil {
		t.Fatal(err)
	}

	// status=paid returns exactly the paid order.
	rec := call(t, svc.ListOrders, http.MethodGet, "/api/console/orders?status=paid", nil, uid)
	var list orderListResponse
	decodeBody(t, rec, &list)
	if len(list.Items) != 1 || list.Items[0].ID != paid.ID {
		t.Fatalf("status=paid items = %+v, want only order %d", list.Items, paid.ID)
	}

	// status=pending + the closed order's number is an empty intersection.
	rec = call(t, svc.ListOrders, http.MethodGet,
		"/api/console/orders?status=pending&q="+closed.OrderNo, nil, uid)
	decodeBody(t, rec, &list)
	if len(list.Items) != 0 {
		t.Fatalf("pending+closed-number items = %d, want 0 (intersection)", len(list.Items))
	}

	// status=pending + its own number returns it.
	rec = call(t, svc.ListOrders, http.MethodGet,
		"/api/console/orders?status=pending&q="+pending.OrderNo, nil, uid)
	decodeBody(t, rec, &list)
	if len(list.Items) != 1 || list.Items[0].ID != pending.ID {
		t.Fatalf("pending+own-number items = %+v, want order %d", list.Items, pending.ID)
	}

	// status=closed returns the closed one.
	rec = call(t, svc.ListOrders, http.MethodGet, "/api/console/orders?status=closed", nil, uid)
	decodeBody(t, rec, &list)
	if len(list.Items) != 1 || list.Items[0].ID != closed.ID {
		t.Fatalf("status=closed items = %+v, want order %d", list.Items, closed.ID)
	}

	// An unknown status is a query error, not "no filter".
	rec = call(t, svc.ListOrders, http.MethodGet, "/api/console/orders?status=bogus", nil, uid)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=bogus code = %d, want 400", rec.Code)
	}
	if body := errorBody(t, rec); body.Error.Code != "invalid_query_parameter" {
		t.Fatalf("status=bogus code = %q, want invalid_query_parameter", body.Error.Code)
	}
}

// Idempotency-Key: the second POST inside the window returns the first order
// instead of creating another payable row.
func TestCreateOrderIdempotencyKey(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	plan := planByCode(t, "STANDARD")
	body := map[string]any{
		"order_type": OrderTypePlan, "plan_id": plan.ID, "pay_channel": PayChannelWechat,
	}

	first, rec1 := createOrderWithKey(t, svc, uid, body, "idem-key-1")
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first status = %d", rec1.Code)
	}
	second, rec2 := createOrderWithKey(t, svc, uid, body, "idem-key-1")
	if rec2.Code != http.StatusOK {
		t.Fatalf("second status = %d body=%s", rec2.Code, rec2.Body.String())
	}
	if second.ID != first.ID {
		t.Fatalf("second returned order %d, want the first %d", second.ID, first.ID)
	}
	var n int
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_recharge_order WHERE user_id = ?`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("orders = %d, want 1", n)
	}
}

func createOrderWithKey(t *testing.T, svc *Service, uid int64, body map[string]any, key string) (orderView, *httptest.ResponseRecorder) {
	t.Helper()
	rec := callWithHeaders(t, svc.CreateOrder, http.MethodPost, "/api/console/orders",
		body, uid, map[string]string{"Idempotency-Key": key})
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("create order status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp createOrderResponse
	decodeBody(t, rec, &resp)
	return resp.Order, rec
}

// Plans: the free default and the trial tier are not purchasable, and every
// number matches t_plan (no hard-coding in the handler).
func TestPlansComeFromTable(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	rec := call(t, svc.Plans, http.MethodGet, "/api/console/plans", nil, uid)
	if rec.Code != http.StatusOK {
		t.Fatalf("plans status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp planListResponse
	decodeBody(t, rec, &resp)
	if len(resp.Items) < 5 {
		t.Fatalf("plans = %d, want at least the 5 seeded tiers", len(resp.Items))
	}
	byCode := map[string]planView{}
	for _, p := range resp.Items {
		byCode[p.Code] = p
	}
	free, ok := byCode["FREE_DEFAULT"]
	if !ok {
		t.Fatal("FREE_DEFAULT missing from the plan list")
	}
	if free.Purchasable {
		t.Fatal("FREE_DEFAULT must not be purchasable (hard constraint 8)")
	}
	if byCode["TRIAL"].Purchasable {
		t.Fatal("TRIAL is granted, not sold")
	}
	standard := byCode["STANDARD"]
	if !standard.Purchasable || standard.PriceFen != 9900 ||
		standard.GrantRechargeMicro != 99000000 || standard.GrantBonusMicro != 20000000 ||
		standard.RPM != 300 || standard.TPM != 630000 || standard.Concurrency != 110 {
		t.Fatalf("STANDARD view = %+v, want the §3.2.1 numbers", standard)
	}
	if !standard.Permanent {
		t.Fatal("paid tiers are permanent (permanent=1)")
	}
	if resp.DefaultTier != "FREE_DEFAULT" || resp.MicroPerCredit != MicroPerCredit {
		t.Fatalf("meta = %+v", resp)
	}
	t.Logf("plans=%d default=%s trial_days=%d bonus_days=%d STANDARD=%d/%d/%d fen=%d",
		len(resp.Items), resp.DefaultTier, resp.TrialDays, resp.BonusExpireDays,
		standard.RPM, standard.TPM, standard.Concurrency, standard.PriceFen)
}

// Redeem: a code credits once, the second attempt is rejected, and the credit
// is a non-refundable batch plus one REDEEM ledger entry.
func TestRedeemCodeSingleUse(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	plain, _, _ := seedRedeemBatch(t, uid, 1000, time.Now().UTC().Add(24*time.Hour))

	rec := call(t, svc.Redeem, http.MethodPost, "/api/console/redeem", map[string]any{"code": plain}, uid)
	if rec.Code != http.StatusOK {
		t.Fatalf("redeem status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp redeemResponse
	decodeBody(t, rec, &resp)
	if resp.AmountMicro != fenToMicro(1000) || resp.CreditTarget != "BONUS" {
		t.Fatalf("redeem response = %+v", resp)
	}
	if b := balanceOf(t, uid); b.Bonus != fenToMicro(1000) || b.Recharge != 0 {
		t.Fatalf("balance after redeem = %+v, want bonus %d", b, fenToMicro(1000))
	}
	var kind string
	var refundable int64
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT batch_type, refundable FROM t_credit_batch WHERE user_id = ?`, uid).
		Scan(&kind, &refundable); err != nil {
		t.Fatal(err)
	}
	if kind != "REDEEM" || refundable != 0 {
		t.Fatalf("redeem batch = %s/%d, want REDEEM/0", kind, refundable)
	}
	var bizType string
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT biz_type FROM t_ledger_entry WHERE user_id = ?`, uid).Scan(&bizType); err != nil {
		t.Fatal(err)
	}
	if bizType != ledger.BizTypeRedeem {
		t.Fatalf("ledger biz_type = %q, want REDEEM", bizType)
	}

	rec = call(t, svc.Redeem, http.MethodPost, "/api/console/redeem", map[string]any{"code": plain}, uid)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("second redeem status = %d, want 400", rec.Code)
	}
	if b := balanceOf(t, uid); b.Total != fenToMicro(1000) {
		t.Fatalf("balance after double redeem = %d, want unchanged %d", b.Total, fenToMicro(1000))
	}
}

// The balance mirror (K26) must be dropped after a credit so the gateway does
// not admit a request against the pre-payment balance.
func TestCallbackInvalidatesBalanceMirror(t *testing.T) {
	svc, rdb := newTestService(t)
	uid := newTestUser(t, nil)
	plan := planByCode(t, "STANDARD")
	order, _ := createOrder(t, svc, uid, map[string]any{
		"order_type": OrderTypePlan, "plan_id": plan.ID, "pay_channel": PayChannelWechat,
	})

	ctx := context.Background()
	key := redisx.BalanceKey(uid)
	if err := rdb.Raw().Set(ctx, key, 0, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if rec := successfulCallback(t, svc, order, "WXK26"); rec.Code != http.StatusOK {
		t.Fatalf("callback status = %d", rec.Code)
	}
	if n, err := rdb.Raw().Exists(ctx, key).Result(); err != nil || n != 0 {
		t.Fatalf("K26 exists=%d err=%v, want 0 after a credit", n, err)
	}
}

// unexported helper retained for the batch test's SQL scanning path.
