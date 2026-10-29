package dispatch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/fakeupstream"
	"yunshu.dev/core/internal/gw"
	"yunshu.dev/core/internal/settle"
)

type fakeEnqueuer struct {
	mu    sync.Mutex
	calls []settle.EnqueueParams
	err   error
}

func (f *fakeEnqueuer) Enqueue(_ context.Context, p settle.EnqueueParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, p)
	return f.err
}

func (f *fakeEnqueuer) all() []settle.EnqueueParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]settle.EnqueueParams(nil), f.calls...)
}

type usageRecorder struct {
	mu   sync.Mutex
	rows []settle.UsageRecord
}

func (r *usageRecorder) add(u settle.UsageRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, u)
}

func (r *usageRecorder) all() []settle.UsageRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]settle.UsageRecord(nil), r.rows...)
}

func newSettleDispatcher(t *testing.T, led *fakeLedger, enq *fakeEnqueuer, usage *usageRecorder) (*Dispatcher, *attemptRecorder) {
	t.Helper()
	up := httptest.NewServer(fakeupstream.New(fakeupstream.Options{Mode: fakeupstream.ModeOK}))
	t.Cleanup(up.Close)
	_, envelope := testBox(t)
	cat := testCatalog(t, up.URL+"/v1", envelope, nil)
	attempts := &attemptRecorder{}
	d := newDispatcher(t, cat, led, attempts, &fakeIdem{})
	d.opts.SettleEnqueuer = enq
	d.opts.UsageSink = usage.add
	return d, attempts
}

// P26 happy path: the request enqueues and does not call the ledger
// synchronously. Exactly one usage row and one attempt row are emitted.
func TestP26EnqueuesInsteadOfSettlingSynchronously(t *testing.T) {
	led := &fakeLedger{}
	enq := &fakeEnqueuer{}
	usage := &usageRecorder{}
	d, attempts := newSettleDispatcher(t, led, enq, usage)

	rec := serve(t, d, testCtx(false))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	if n := len(attempts.all()); n != 1 {
		t.Fatalf("attempt rows = %d, want exactly 1", n)
	}

	calls := enq.all()
	if len(calls) != 1 {
		t.Fatalf("enqueue calls = %d, want 1", len(calls))
	}
	if calls[0].RequestID == "" || calls[0].ConsumedMicro <= 0 {
		t.Fatalf("enqueue params = %+v", calls[0])
	}
	if led.settleCount() != 0 {
		t.Fatalf("synchronous settle calls = %d, want 0 when enqueue succeeded", led.settleCount())
	}

	rows := usage.all()
	if len(rows) != 1 {
		t.Fatalf("usage rows = %d, want exactly 1", len(rows))
	}
	row := rows[0]
	if row.Result != "SUCCESS" || row.HTTPStatus != 200 {
		t.Fatalf("usage result/status = %s/%d", row.Result, row.HTTPStatus)
	}
	if row.ConsumedMicro != calls[0].ConsumedMicro {
		t.Fatalf("usage consumed = %d, enqueue consumed = %d", row.ConsumedMicro, calls[0].ConsumedMicro)
	}
	if row.PreholdMicro != 1000 {
		t.Fatalf("usage prehold = %d, want 1000", row.PreholdMicro)
	}
	if row.UpstreamAttempts != 1 {
		t.Fatalf("usage upstream_attempts = %d, want 1", row.UpstreamAttempts)
	}
	if row.ChannelID != 10 || row.RouteID != 1 {
		t.Fatalf("usage channel/route = %d/%d", row.ChannelID, row.RouteID)
	}
}

// P28 copies the cfg:ver the gateway stamped on the Ctx into the usage row.
// Without this the column stays 0 for every live request and R09 has only seed
// rows to reconcile against.
func TestP28UsageRecordCarriesConfigVersion(t *testing.T) {
	led := &fakeLedger{}
	enq := &fakeEnqueuer{}
	usage := &usageRecorder{}
	d, _ := newSettleDispatcher(t, led, enq, usage)

	if got := d.opts.Catalog.Version(); got != testConfigVersion {
		t.Fatalf("catalog version = %d, want %d", got, testConfigVersion)
	}

	c := testCtx(false)
	// gw.Handler stamps this at ingress; dispatch tests bypass the handler and
	// take the same value from the catalog the gateway would query.
	c.ConfigVersion = d.opts.Catalog.Version()
	if rec := serve(t, d, c); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	rows := usage.all()
	if len(rows) != 1 {
		t.Fatalf("usage rows = %d, want exactly 1", len(rows))
	}
	if got := rows[0].ConfigVersion; got != testConfigVersion || got == 0 {
		t.Fatalf("usage config_version = %d, want %d and non-zero", got, testConfigVersion)
	}
}

// An enqueue failure must fall back to the synchronous ledger call, not fail
// the request: a hold with no persisted task would otherwise stay HELD.
func TestP26EnqueueFailureFallsBackToSynchronousSettle(t *testing.T) {
	led := &fakeLedger{}
	enq := &fakeEnqueuer{err: errors.New("db down")}
	usage := &usageRecorder{}
	d, _ := newSettleDispatcher(t, led, enq, usage)

	rec := serve(t, d, testCtx(false))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if led.settleCount() != 1 {
		t.Fatalf("synchronous settle calls = %d, want 1 after enqueue failure", led.settleCount())
	}
	rows := usage.all()
	if len(rows) != 1 || rows[0].Result != "SUCCESS" {
		t.Fatalf("usage rows = %+v, want one SUCCESS row", rows)
	}
}

// Both paths failing is the only case that may surface settlement_unavailable.
func TestP26BothPathsFailReturns503(t *testing.T) {
	led := &fakeLedger{err: errors.New("ledger down")}
	enq := &fakeEnqueuer{err: errors.New("db down")}
	usage := &usageRecorder{}
	d, _ := newSettleDispatcher(t, led, enq, usage)

	rec := serve(t, d, testCtx(false))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}
	body := errorCode(t, rec)
	if body["code"] != apierr.CodeSettlementUnavailable {
		t.Fatalf("code = %v, want %s", body["code"], apierr.CodeSettlementUnavailable)
	}
	rows := usage.all()
	if len(rows) != 1 {
		t.Fatalf("usage rows = %d, want exactly 1", len(rows))
	}
	if rows[0].Result != "FAILED" || rows[0].BizCode != apierr.CodeSettlementUnavailable {
		t.Fatalf("usage row = %+v", rows[0])
	}
	if rows[0].ConsumedMicro != 0 {
		t.Fatalf("consumed = %d, want 0 on failure", rows[0].ConsumedMicro)
	}
}

// A request rejected before any upstream attempt still produces exactly one
// usage row, with the failure attributed to the route stage.
func TestP28UsageRowEmittedWhenNoTargetExists(t *testing.T) {
	usage := &usageRecorder{}
	_, envelope := testBox(t)
	cat := testCatalog(t, "http://127.0.0.1:1/v1", envelope, func(ch *gw.ChannelEntry) {
		ch.BaseURL = ""
	})
	led := &fakeLedger{}
	d := newDispatcher(t, cat, led, &attemptRecorder{}, &fakeIdem{})
	d.opts.UsageSink = usage.add

	rec := serve(t, d, testCtx(false))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}
	rows := usage.all()
	if len(rows) != 1 {
		t.Fatalf("usage rows = %d, want exactly 1", len(rows))
	}
	if rows[0].Result != "FAILED" || rows[0].FailStage != "ROUTE" {
		t.Fatalf("usage row = %+v", rows[0])
	}
}
