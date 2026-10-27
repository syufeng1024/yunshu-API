package ops

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"yunshu.dev/core/internal/jsonx"
)

// newLoginUser inserts an account with a real bcrypt hash so the user-side
// login handler can reach the status check. newTestUser's placeholder hash
// cannot pass password verification.
func newLoginUser(t *testing.T, password string) (int64, string) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	now := time.Now()
	userNo := fmt.Sprintf("L%s%03d", now.Format("20060102"), now.UnixNano()%1000)
	email := fmt.Sprintf("t15-login-%d@test.local", now.UnixNano())
	res, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_user
			(user_no, email, password_hash, password_algo, password_updated_at,
			 nickname, role, status, created_at, updated_at)
		VALUES (?,?,?,'bcrypt',?,'t15-login','PERSONAL','NORMAL',?,?)`,
		userNo, email, string(hash), now, now, now)
	if err != nil {
		t.Fatalf("insert login user: %v", err)
	}
	uid, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_admin_audit_log WHERE target_id = ?`, fmt.Sprint(uid))
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_credit_account WHERE user_id = ?`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_user WHERE id = ?`, uid)
	})
	return uid, email
}

// Hard constraint 13: the operator's disable reason is returned by the admin
// API and never leaks into the customer-facing account_disabled body.
func TestDisableReasonIsOperatorOnly(t *testing.T) {
	svc := newTestService(t)
	opsUID := newTestUser(t, "OPS")
	target, email := newLoginUser(t, "Passw0rd!x")
	const reason = "风控命中：异常退款-敏感原因-XYZ"

	rec := call(t, svc.DisableUser, http.MethodPost,
		fmt.Sprintf("/api/admin/users/%d/disable", target),
		map[string]any{"reason": reason},
		testCallOptions{uid: opsUID, role: "OPS", pathVal: map[string]string{"id": fmt.Sprint(target)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", rec.Code, rec.Body.String())
	}
	var detail userDetailView
	decodeBody(t, rec, &detail)
	if detail.User.DisableReason != reason {
		t.Fatalf("operator response reason = %q, want %q", detail.User.DisableReason, reason)
	}
	if detail.User.DisableSource != disableSourceOpsManual {
		t.Fatalf("disable_source = %q, want OPS_MANUAL", detail.User.DisableSource)
	}

	// The admin detail view also carries the reason.
	rec = call(t, svc.GetUser, http.MethodGet,
		fmt.Sprintf("/api/admin/users/%d", target), nil,
		testCallOptions{uid: opsUID, role: "OPS", pathVal: map[string]string{"id": fmt.Sprint(target)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("admin detail status=%d body=%s", rec.Code, rec.Body.String())
	}
	var adminDetail userDetailView
	decodeBody(t, rec, &adminDetail)
	if adminDetail.User.DisableReason != reason {
		t.Fatalf("admin detail reason = %q, want %q", adminDetail.User.DisableReason, reason)
	}

	// User-side login sees the generic copy, with no trace of the category.
	loginSvc := newLoginService(t)
	payload, _ := jsonx.Marshal(map[string]string{"identifier": email, "password": "Passw0rd!x"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(payload))
	loginRec := httptest.NewRecorder()
	loginSvc.Login(loginRec, req)
	if loginRec.Code != http.StatusForbidden {
		t.Fatalf("login status=%d body=%s, want 403", loginRec.Code, loginRec.Body.String())
	}
	body := errorBody(t, loginRec)
	if body.Error.Code != "account_disabled" {
		t.Fatalf("login code=%q, want account_disabled", body.Error.Code)
	}
	if body.Error.Message != "账号已被禁用，请联系客服" {
		t.Fatalf("user-facing message = %q, want the generic copy", body.Error.Message)
	}
	if strings.Contains(loginRec.Body.String(), reason) || strings.Contains(loginRec.Body.String(), "敏感原因") {
		t.Fatalf("user-facing body leaked the operator reason: %s", loginRec.Body.String())
	}
	t.Logf("admin sees reason=%q; user-side login message=%q", detail.User.DisableReason, body.Error.Message)
}

func TestEnableUserClearsDisableFields(t *testing.T) {
	svc := newTestService(t)
	opsUID := newTestUser(t, "OPS")
	target := newTestUser(t, "PERSONAL")

	rec := call(t, svc.DisableUser, http.MethodPost,
		fmt.Sprintf("/api/admin/users/%d/disable", target),
		map[string]any{"reason": "测试禁用"},
		testCallOptions{uid: opsUID, role: "OPS", pathVal: map[string]string{"id": fmt.Sprint(target)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = call(t, svc.EnableUser, http.MethodPost,
		fmt.Sprintf("/api/admin/users/%d/enable", target), nil,
		testCallOptions{uid: opsUID, role: "OPS", pathVal: map[string]string{"id": fmt.Sprint(target)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("enable status=%d body=%s", rec.Code, rec.Body.String())
	}
	var (
		status, source, reason sql.NullString
		disabledBy             sql.NullInt64
	)
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT status, disable_source, disable_reason, disabled_by FROM t_user WHERE id = ?`,
		target).Scan(&status, &source, &reason, &disabledBy); err != nil {
		t.Fatal(err)
	}
	if status.String != userStatusNormal {
		t.Fatalf("status = %q, want NORMAL", status.String)
	}
	if source.Valid || reason.Valid || disabledBy.Valid {
		t.Fatalf("disable fields not cleared: source=%v reason=%v by=%v", source, reason, disabledBy)
	}
}

func TestUserListFilters(t *testing.T) {
	svc := newTestService(t)
	opsUID := newTestUser(t, "OPS")
	target := newTestUser(t, "PERSONAL")
	if _, err := testDB.ExecContext(context.Background(),
		`UPDATE t_user SET status = 'DISABLED', disable_source = 'OPS_MANUAL' WHERE id = ?`, target); err != nil {
		t.Fatal(err)
	}
	var userNo string
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT user_no FROM t_user WHERE id = ?`, target).Scan(&userNo); err != nil {
		t.Fatal(err)
	}

	rec := call(t, svc.ListUsers, http.MethodGet,
		"/api/admin/users?status=DISABLED&user_no="+userNo, nil,
		testCallOptions{uid: opsUID, role: "OPS"})
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var list userListResponse
	decodeBody(t, rec, &list)
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].ID != target {
		t.Fatalf("filter result total=%d items=%+v, want only user %d", list.Total, list.Items, target)
	}
}
