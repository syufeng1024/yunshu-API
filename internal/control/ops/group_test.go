package ops

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"yunshu.dev/core/internal/redisx"
)

type fakeKeyAuth struct {
	calls  int
	hashes []string
	gp     int64
	done   chan struct{}
}

func (f *fakeKeyAuth) Upsert(context.Context, string, redisx.KeyAuthRecord) error { return nil }
func (f *fakeKeyAuth) Delete(context.Context, string) error                       { return nil }
func (f *fakeKeyAuth) BulkUpdateGroupRatio(_ context.Context, hashes []string, gp int64) error {
	f.calls++
	f.hashes = append([]string(nil), hashes...)
	f.gp = gp
	if f.done != nil {
		select {
		case f.done <- struct{}{}:
		default:
		}
	}
	return nil
}

// A16: 0.85 stores 8500; 1.2 is refused; internal 0 is the only zero.
func TestGroupRatioBoundsAndK13WriteThrough(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	keyUser := newTestUser(t, "PERSONAL")
	fake := &fakeKeyAuth{done: make(chan struct{}, 1)}
	svc.KeyAuth = fake

	var contractID, internalID int64
	var contractBP int64
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT id, ratio_bp FROM t_price_group WHERE code = 'contract'`).Scan(&contractID, &contractBP); err != nil {
		t.Fatalf("load contract group: %v", err)
	}
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT id FROM t_price_group WHERE code = 'internal'`).Scan(&internalID); err != nil {
		t.Fatalf("load internal group: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testDB.ExecContext(ctx, `UPDATE t_price_group SET ratio_bp = ? WHERE id = ?`, contractBP, contractID)
		_, _ = testDB.ExecContext(ctx, `UPDATE t_price_group SET ratio_bp = 0 WHERE id = ?`, internalID)
	})
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(),
			`DELETE FROM t_api_key WHERE key_name = 't15-group-key'`)
	})

	hash := strings.Repeat("ab", 32)
	if _, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_api_key
			(user_id, key_name, key_prefix, key_hash, key_last4, status, group_code, created_at, updated_at)
		VALUES (?, 't15-group-key', 'sk-test', ?, '1234', 'ENABLED', 'contract', NOW(3), NOW(3))`,
		keyUser, hash); err != nil {
		t.Fatalf("insert api key: %v", err)
	}

	// 0.8 is a deliberate change from the seeded 0.85, so the K13 write-through
	// must fire with gp=8000.
	rec := call(t, svc.UpdateGroup, http.MethodPatch, fmt.Sprintf("/api/admin/groups/%d", contractID),
		map[string]any{"ratio": 0.8},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": fmt.Sprint(contractID)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("ratio 0.8 status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	var detail groupDetailResponse
	decodeBody(t, rec, &detail)
	if detail.Group.RatioBP != 8000 {
		t.Fatalf("ratio_bp = %d, want 8000", detail.Group.RatioBP)
	}
	var stored int64
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT ratio_bp FROM t_price_group WHERE id = ?`, contractID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 8000 {
		t.Fatalf("stored ratio = %d, want 8000", stored)
	}
	// The fan-out is asynchronous: wait for the K13 writer, then verify.
	select {
	case <-fake.done:
	case <-time.After(5 * time.Second):
		t.Fatal("K13 write-through did not run within 5s")
	}
	if fake.calls != 1 || fake.gp != 8000 {
		t.Fatalf("K13 calls=%d gp=%d, want 1 call at 8000", fake.calls, fake.gp)
	}
	if len(fake.hashes) != 1 || fake.hashes[0] != hash[:16] {
		t.Fatalf("K13 hash=%v, want [%s]", fake.hashes, hash[:16])
	}
	if !detail.KeySync.OK || !strings.Contains(detail.KeySync.Note, "异步") {
		t.Fatalf("key_sync = %+v, want an accepted async fan-out", detail.KeySync)
	}
	t.Logf("A16 ratio 0.8 → DB ratio_bp=8000, K13 gp=%d hashes=%v", fake.gp, fake.hashes)

	// 0.85 stores 8500.
	rec = call(t, svc.UpdateGroup, http.MethodPatch, fmt.Sprintf("/api/admin/groups/%d", contractID),
		map[string]any{"ratio": 0.85},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": fmt.Sprint(contractID)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("ratio 0.85 status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT ratio_bp FROM t_price_group WHERE id = ?`, contractID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 8500 {
		t.Fatalf("stored ratio = %d, want 8500", stored)
	}
	t.Logf("A16 ratio 0.85 → DB ratio_bp=8500")

	// 1.2 exceeds 10000 bp and is refused.
	rec = call(t, svc.UpdateGroup, http.MethodPatch, fmt.Sprintf("/api/admin/groups/%d", contractID),
		map[string]any{"ratio": 1.2},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": fmt.Sprint(contractID)}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ratio 1.2 status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if !strings.Contains(errorBody(t, rec).Error.Message, "不能大于 1.0") {
		t.Fatalf("ratio 1.2 message = %q", errorBody(t, rec).Error.Message)
	}
	t.Logf("A16 ratio 1.2 → HTTP 400: %s", errorBody(t, rec).Error.Message)

	// A zero ratio outside internal is refused.
	rec = call(t, svc.UpdateGroup, http.MethodPatch, fmt.Sprintf("/api/admin/groups/%d", contractID),
		map[string]any{"ratio": 0},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": fmt.Sprint(contractID)}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ratio 0 status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}

	// internal keeps its 0 exception; a non-zero ratio is refused.
	rec = call(t, svc.UpdateGroup, http.MethodPatch, fmt.Sprintf("/api/admin/groups/%d", internalID),
		map[string]any{"ratio": 0},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": fmt.Sprint(internalID)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("internal ratio 0 status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	t.Logf("A16 internal ratio 0 → HTTP 200 (exception)")
	rec = call(t, svc.UpdateGroup, http.MethodPatch, fmt.Sprintf("/api/admin/groups/%d", internalID),
		map[string]any{"ratio": 0.85},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": fmt.Sprint(internalID)}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("internal ratio 0.85 status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
}
