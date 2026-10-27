package usage

import (
	"net/http"
	"testing"
	"time"
)

// A3 / F9-2: 12,345 credits is 123,450,000 micro. fen = (123450000+5000)/10000
// = 12345, so amount_yuan is "123.45" and the front end never divides. The
// split columns must add up to the total. The failed row in the same month
// counts in request_total (2 requests, 1 success, 5000 bp) but must not move
// any money column.
func TestBillAmountRoundsToFenAndKeepsSplit(t *testing.T) {
	uid := newTestUser(t)
	svc := newTestService(t, newFakeClock(testBase))

	seedUsage(t, usageSeed{
		uid: uid, modelID: 3, channelID: 7, channelName: "aliyun-primary",
		consumed: 123_450_000, recharge: 100_000_000, bonus: 23_450_000,
		createdAt: testBase.Add(-time.Hour),
	})
	seedUsage(t, usageSeed{
		uid: uid, modelID: 3, result: "FAILED", httpCode: http.StatusBadGateway,
		consumed: 0, createdAt: testBase.Add(-2 * time.Hour),
	})

	rec := callHandler(t, svc.Bill, "/api/console/bills?month=2026-10", uid)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out billResponse
	decodeBody(t, rec, &out)
	if out.AmountFen != 12_345 {
		t.Fatalf("amount_fen=%d want 12345", out.AmountFen)
	}
	if out.AmountYuan != "123.45" {
		t.Fatalf("amount_yuan=%q want \"123.45\"", out.AmountYuan)
	}
	if out.ConsumedMicro != 123_450_000 {
		t.Fatalf("consumed_micro=%d want 123450000", out.ConsumedMicro)
	}
	if out.ConsumedRechargeMicro != 100_000_000 || out.ConsumedBonusMicro != 23_450_000 {
		t.Fatalf("split=%d/%d want 100000000/23450000",
			out.ConsumedRechargeMicro, out.ConsumedBonusMicro)
	}
	if out.ConsumedRechargeMicro+out.ConsumedBonusMicro != out.ConsumedMicro {
		t.Fatalf("split sum %d != total %d", out.ConsumedRechargeMicro+out.ConsumedBonusMicro,
			out.ConsumedMicro)
	}
	if out.RequestTotal != 2 || out.RequestSuccess != 1 || out.SuccessRateBP != 5_000 {
		t.Fatalf("requests=%d/%d rate=%d want 2/1/5000; FAILED must count in the denominator only",
			out.RequestTotal, out.RequestSuccess, out.SuccessRateBP)
	}
	t.Logf("A3 micro=%d → fen=%d → yuan=%s (split %d+%d, requests %d/%d rate_bp=%d)",
		out.ConsumedMicro, out.AmountFen, out.AmountYuan,
		out.ConsumedRechargeMicro, out.ConsumedBonusMicro,
		out.RequestTotal, out.RequestSuccess, out.SuccessRateBP)
}

// The month boundary is 00:00 CST, not UTC. 2026-09-30T15:59:59.999Z is still
// September in Shanghai; 2026-09-30T16:00:00Z is October.
func TestBillMonthBoundaryIsCST(t *testing.T) {
	uid := newTestUser(t)
	svc := newTestService(t, newFakeClock(testBase))

	seedUsage(t, usageSeed{
		uid: uid, modelID: 3, channelID: 7, channelName: "aliyun-primary",
		consumed: 1_000_000, recharge: 1_000_000,
		createdAt: time.Date(2026, 9, 30, 15, 59, 59, 999_000_000, time.UTC),
	})
	seedUsage(t, usageSeed{
		uid: uid, modelID: 3, channelID: 7, channelName: "aliyun-primary",
		consumed: 2_000_000, recharge: 2_000_000,
		createdAt: time.Date(2026, 9, 30, 16, 0, 0, 0, time.UTC),
	})

	oct := callHandler(t, svc.Bill, "/api/console/bills?month=2026-10", uid)
	if oct.Code != http.StatusOK {
		t.Fatalf("october status=%d body=%s", oct.Code, oct.Body.String())
	}
	var octOut billResponse
	decodeBody(t, oct, &octOut)
	if octOut.ConsumedMicro != 2_000_000 || octOut.RequestTotal != 1 {
		t.Fatalf("october = %d micro / %d requests, want 2000000/1",
			octOut.ConsumedMicro, octOut.RequestTotal)
	}

	sep := callHandler(t, svc.Bill, "/api/console/bills?month=2026-09", uid)
	if sep.Code != http.StatusOK {
		t.Fatalf("september status=%d body=%s", sep.Code, sep.Body.String())
	}
	var sepOut billResponse
	decodeBody(t, sep, &sepOut)
	if sepOut.ConsumedMicro != 1_000_000 || sepOut.RequestTotal != 1 {
		t.Fatalf("september = %d micro / %d requests, want 1000000/1",
			sepOut.ConsumedMicro, sepOut.RequestTotal)
	}
	t.Logf("CST boundary: oct start=%s end=%s; sep micro=%d oct micro=%d",
		octOut.PeriodStart, octOut.PeriodEnd, sepOut.ConsumedMicro, octOut.ConsumedMicro)
}

// The conversion itself: integer half-up, no float. 1 credit = 10,000 micro,
// 1 yuan = 100 credit = 1,000,000 micro.
func TestFenConversionIsIntegerHalfUp(t *testing.T) {
	cases := []struct {
		micro int64
		fen   int64
		yuan  string
	}{
		{0, 0, "0.00"},
		{4_999, 0, "0.00"},  // 0.4999 fen rounds down
		{5_000, 1, "0.01"},  // exactly half a fen rounds up
		{10_000, 1, "0.01"}, // 1 credit = 1 fen
		{123_450_000, 12_345, "123.45"},
		{1_000_000, 100, "1.00"},
	}
	for _, c := range cases {
		if got := fenFromMicro(c.micro); got != c.fen {
			t.Errorf("fenFromMicro(%d)=%d want %d", c.micro, got, c.fen)
		}
		if got := yuanFromFen(c.fen); got != c.yuan {
			t.Errorf("yuanFromFen(%d)=%q want %q", c.fen, got, c.yuan)
		}
	}
}

func TestBillRejectsMalformedMonth(t *testing.T) {
	uid := newTestUser(t)
	svc := newTestService(t, newFakeClock(testBase))
	for _, month := range []string{"2026-13", "2026-1", "202610", "abc"} {
		rec := callHandler(t, svc.Bill, "/api/console/bills?month="+month, uid)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("month=%q status=%d body=%s want 400", month, rec.Code, rec.Body.String())
		}
		if got := errorOf(t, rec).Error.Code; got != "invalid_query_parameter" {
			t.Fatalf("month=%q code=%q want invalid_query_parameter", month, got)
		}
	}
}
