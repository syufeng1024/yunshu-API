package ops

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"net/http"
	"testing"
	"time"
)

func insertAPIKey(t *testing.T, userID int64, status string, deleted bool) {
	t.Helper()
	now := time.Now().UTC()
	var deletedAt any
	if deleted {
		deletedAt = now
	}
	var raw [32]byte
	if _, err := crand.Read(raw[:]); err != nil {
		t.Fatalf("key hash entropy: %v", err)
	}
	hash := hex.EncodeToString(raw[:])
	if _, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_api_key
			(user_id, key_name, key_prefix, key_hash, key_last4, status,
			 model_scope_mode, ip_whitelist_mode, deleted_at, created_at, updated_at)
		VALUES (?, ?, 'sk-t24xx', ?, '0001', ?, 'ALL', 'OFF', ?, ?, ?)`,
		userID, "t24-key-"+uniqueSuffix(), hash, status, deletedAt, now, now); err != nil {
		t.Fatalf("insert api key: %v", err)
	}
}

// A5: key_count counts only ENABLED, non-deleted keys; a user with none is 0.
// The list path exercises the page-level GROUP BY; the detail path exercises
// the single-user fill.
func TestAdminUserKeyCount(t *testing.T) {
	svc := newTestService(t)
	admin := newTestUser(t, "OPS")
	target := newTestUser(t, "PERSONAL")
	empty := newTestUser(t, "PERSONAL")
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(),
			`DELETE FROM t_api_key WHERE user_id IN (?, ?)`, target, empty)
	})

	insertAPIKey(t, target, "ENABLED", false)
	insertAPIKey(t, target, "ENABLED", false)
	insertAPIKey(t, target, "DISABLED", false)
	insertAPIKey(t, target, "ENABLED", true)

	rec := call(t, svc.GetUser, http.MethodGet, "/api/admin/users/"+itoa(target), nil,
		testCallOptions{uid: admin, role: "OPS", pathVal: map[string]string{"id": itoa(target)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("get user status=%d body=%s", rec.Code, rec.Body.String())
	}
	var detail userDetailView
	decodeBody(t, rec, &detail)
	if detail.User.KeyCount != 2 {
		t.Fatalf("detail key_count=%d, want 2 (2 ENABLED, 1 DISABLED, 1 deleted)",
			detail.User.KeyCount)
	}

	var userNo string
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT user_no FROM t_user WHERE id = ?`, target).Scan(&userNo); err != nil {
		t.Fatalf("read user_no: %v", err)
	}
	rec = call(t, svc.ListUsers, http.MethodGet,
		"/api/admin/users?user_no="+userNo+"&page=1&page_size=20", nil,
		testCallOptions{uid: admin, role: "OPS"})
	if rec.Code != http.StatusOK {
		t.Fatalf("list users status=%d body=%s", rec.Code, rec.Body.String())
	}
	var list userListResponse
	decodeBody(t, rec, &list)
	found := false
	for _, item := range list.Items {
		if item.ID != target {
			continue
		}
		found = true
		if item.KeyCount != 2 {
			t.Fatalf("list key_count=%d, want 2", item.KeyCount)
		}
	}
	if !found {
		t.Fatalf("user %d missing from list", target)
	}

	rec = call(t, svc.GetUser, http.MethodGet, "/api/admin/users/"+itoa(empty), nil,
		testCallOptions{uid: admin, role: "OPS", pathVal: map[string]string{"id": itoa(empty)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("get empty user status=%d body=%s", rec.Code, rec.Body.String())
	}
	var emptyDetail userDetailView
	decodeBody(t, rec, &emptyDetail)
	if emptyDetail.User.KeyCount != 0 {
		t.Fatalf("empty user key_count=%d, want 0", emptyDetail.User.KeyCount)
	}
}
