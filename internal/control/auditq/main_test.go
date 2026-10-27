package auditq

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"yunshu.dev/core/internal/control/ctltest"
)

var testDB *sql.DB

func TestMain(m *testing.M) {
	db, err := ctltest.SetupDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "auditq tests: %v\n", err)
		os.Exit(1)
	}
	testDB = db
	code := m.Run()
	_ = db.Close()
	os.Exit(code)
}

// insertAudit writes one row and returns its id. before/after are raw JSON
// strings; "" inserts NULL.
func insertAudit(t *testing.T, operatorID int64, module, action, result string, createdAt time.Time, before, after string) int64 {
	t.Helper()
	var beforeArg, afterArg any
	if before != "" {
		beforeArg = before
	}
	if after != "" {
		afterArg = after
	}
	traceID := fmt.Sprintf("%032x", operatorID)
	res, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_admin_audit_log
			(trace_id, operator_id, operator_role, operator_ip, module, action,
			 target_type, target_id, before_json, after_json, reason, result, created_at)
		VALUES (?,?,'OPS',?,?,?,'t_user',?,?,?,NULL,?,?)`,
		traceID, operatorID, []byte{192, 0, 2, 1}, module, action,
		fmt.Sprint(operatorID), beforeArg, afterArg, result, createdAt)
	if err != nil {
		t.Fatalf("insert audit row: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("audit id: %v", err)
	}
	return id
}
