package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"yunshu.dev/core/internal/platform/clock"
)

// A11: 180-day request bodies, 365-day metadata. Boundary rows are kept
// (cutoff is exclusive), and a second run deletes nothing.
func TestPurgeLogRetentionBoundaries(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 26, 4, 0, 0, 0, time.UTC)
	bodyCutoff := now.AddDate(0, 0, -180)
	metaCutoff := now.AddDate(0, 0, -365)

	// t_request_body is created by migration 0022, which jobs tests apply in
	// TestMain. The fixture must use the real shape: expires_at is what the
	// purge reads, and the table is not dropped here (dropping it would leave
	// schema_migrations claiming 0022 is applied).
	cleanupPurge(t)
	t.Cleanup(func() { cleanupPurge(t) })

	// Body rows: one past the expiry, one exactly on now, one inside the
	// window, plus enough recent rows that EXPLAIN has an index choice.
	// expires_at = created_at + 180 days.
	insertBody := func(i int, createdAt, expiresAt time.Time) {
		t.Helper()
		if _, err := testDB.ExecContext(ctx,
			`INSERT INTO t_request_body
			   (request_id, user_id, ciphertext, body_key_id, created_at, expires_at)
			 VALUES (?,942001,?, 'body-v1', ?, ?)`,
			jobRID("pbdy", i), []byte("x"), createdAt, expiresAt); err != nil {
			t.Fatalf("insert body: %v", err)
		}
	}
	insertBody(1, bodyCutoff.Add(-24*time.Hour), now.Add(-24*time.Hour))
	insertBody(2, bodyCutoff, now)
	insertBody(3, bodyCutoff.Add(24*time.Hour), now.Add(24*time.Hour))
	for i := 0; i < 200; i++ {
		insertBody(100+i, now.Add(-24*time.Hour), now.AddDate(0, 0, 156))
	}

	insertUsage := func(i int, at time.Time) {
		t.Helper()
		if _, err := testDB.ExecContext(ctx,
			`INSERT INTO t_usage_record
			   (request_id, user_id, api_key_id, api_key_name_snap, model_name_snap,
			    request_model_req, inbound_protocol, http_status, result,
			    consumed_micro, total_ms, created_at)
			 VALUES (?,942001,1,'purge','purge','purge','OPENAI_COMPAT',200,'SUCCESS',0,1,?)`,
			jobRID("pusg", i), at); err != nil {
			t.Fatalf("insert usage: %v", err)
		}
	}
	insertAttempt := func(i int, at time.Time) {
		t.Helper()
		if _, err := testDB.ExecContext(ctx,
			`INSERT INTO t_upstream_attempt
			   (request_id, attempt_index, user_id, model_id, route_id, channel_id,
			    channel_name_snap, protocol_code, upstream_model, phase, result, total_ms, created_at)
			 VALUES (?,1,942001,1,1,1,'purge','OPENAI_COMPAT','purge','COMPLETE','SUCCESS',1,?)`,
			jobRID("patt", i), at); err != nil {
			t.Fatalf("insert attempt: %v", err)
		}
	}
	insertUsage(1, metaCutoff.Add(-24*time.Hour))
	insertUsage(2, metaCutoff)
	insertUsage(3, metaCutoff.Add(24*time.Hour))
	insertAttempt(1, metaCutoff.Add(-24*time.Hour))
	insertAttempt(2, metaCutoff)
	insertAttempt(3, metaCutoff.Add(24*time.Hour))
	for i := 0; i < 200; i++ {
		insertUsage(100+i, now.Add(-24*time.Hour))
		insertAttempt(100+i, now.Add(-24*time.Hour))
	}

	insertIdem := func(i int, expires time.Time) {
		t.Helper()
		if _, err := testDB.ExecContext(ctx,
			`INSERT INTO t_idempotency_record
			   (scope, idem_key, user_id, api_key_id, request_id, state, created_at, expires_at)
			 VALUES ('CLIENT_REQUEST',?,942001,1,?,'DONE',?,?)`,
			fmt.Sprintf("purge-idem-%d", i),
			jobRID("pidm", i), now, expires); err != nil {
			t.Fatalf("insert idem: %v", err)
		}
	}
	insertIdem(1, now.Add(-time.Hour))
	for i := 0; i < 201; i++ {
		insertIdem(100+i, now.Add(time.Hour))
	}

	// A13 for cleanup: all four statements are range scans, not full scans.
	plans := []struct {
		name string
		q    string
		args []any
	}{
		{"body delete",
			`DELETE FROM t_request_body WHERE expires_at < ? AND created_at < ? ORDER BY expires_at LIMIT 10000`,
			[]any{now.UTC(), bodyCutoff.UTC()}},
		{"usage delete", `DELETE FROM t_usage_record WHERE created_at < ? ORDER BY created_at LIMIT 5000`, []any{metaCutoff.UTC()}},
		{"attempt delete", `DELETE FROM t_upstream_attempt WHERE created_at < ? ORDER BY created_at LIMIT 5000`, []any{metaCutoff.UTC()}},
		{"idem delete", `DELETE FROM t_idempotency_record WHERE expires_at < ? ORDER BY expires_at LIMIT 5000`, []any{now.UTC()}},
	}
	for _, p := range plans {
		plan := explainJobs(t, p.q, p.args...)
		for _, row := range plan {
			if row.Type == "ALL" {
				t.Fatalf("%s uses type=ALL: %+v", p.name, plan)
			}
		}
		t.Logf("%s: %s", p.name, formatJobPlan(plan))
	}

	task := NewPurgeLog(testDB, nil, clock.NewFixed(now))
	res, err := task.Run(ctx)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if res.BodyDeleted != 1 || res.MetaDeleted != 1 || res.AttemptDeleted != 1 || res.IdemDeleted != 1 {
		t.Fatalf("purge = %+v; want body=1 meta=1 attempt=1 idem=1", res)
	}
	if res.Truncated {
		t.Fatal("purge reported truncation on a small fixture")
	}

	// yunshu_test accumulates rows from other packages; count this fixture's
	// user only so the assertion stays exact without touching foreign data.
	if n := countRows(t, `SELECT COUNT(*) FROM t_request_body WHERE user_id = 942001`); n != 202 {
		t.Fatalf("body rows = %d; want 202 (expired row deleted)", n)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM t_usage_record WHERE user_id = 942001`); n != 202 {
		t.Fatalf("usage rows = %d; want 202 (366-day row deleted)", n)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM t_upstream_attempt WHERE user_id = 942001`); n != 202 {
		t.Fatalf("attempt rows = %d; want 202", n)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM t_idempotency_record WHERE user_id = 942001`); n != 201 {
		t.Fatalf("idempotency rows = %d; want 201", n)
	}

	res, err = task.Run(ctx)
	if err != nil {
		t.Fatalf("second purge: %v", err)
	}
	if res.BodyDeleted+res.MetaDeleted+res.AttemptDeleted+res.IdemDeleted != 0 {
		t.Fatalf("second purge deleted rows: %+v", res)
	}
	t.Logf("A11 body=%d meta=%d attempts=%d idem=%d batches=%d truncated=%t",
		res.BodyDeleted, res.MetaDeleted, res.AttemptDeleted, res.IdemDeleted, res.Batches, res.Truncated)
}

type jobPlanRow struct {
	Table, Type, Key string
	Rows             sql.NullInt64
}

func explainJobs(t *testing.T, q string, args ...any) []jobPlanRow {
	t.Helper()
	rows, err := testDB.QueryContext(context.Background(), "EXPLAIN "+q, args...)
	if err != nil {
		t.Fatalf("explain: %v\nquery: %s", err, q)
	}
	defer rows.Close()
	var out []jobPlanRow
	for rows.Next() {
		var id int
		var selectType, table, partitions, typ, possible, key, keyLen, ref, filtered, extra sql.NullString
		var n sql.NullInt64
		if err := rows.Scan(&id, &selectType, &table, &partitions, &typ, &possible,
			&key, &keyLen, &ref, &n, &filtered, &extra); err != nil {
			t.Fatalf("scan explain: %v", err)
		}
		out = append(out, jobPlanRow{Table: table.String, Type: typ.String, Key: key.String, Rows: n})
	}
	return out
}

func formatJobPlan(plan []jobPlanRow) string {
	parts := make([]string, 0, len(plan))
	for _, p := range plan {
		parts = append(parts, fmt.Sprintf("%s(type=%s,key=%s,rows=%d)", p.Table, p.Type, p.Key, p.Rows.Int64))
	}
	return strings.Join(parts, " ")
}

func countRows(t *testing.T, q string) int {
	t.Helper()
	var n int
	if err := testDB.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func cleanupPurge(t *testing.T) {
	t.Helper()
	for _, q := range []string{
		`DELETE FROM t_usage_record WHERE user_id = 942001`,
		`DELETE FROM t_upstream_attempt WHERE user_id = 942001`,
		`DELETE FROM t_idempotency_record WHERE user_id = 942001`,
		`DELETE FROM t_request_body WHERE user_id = 942001`,
	} {
		if _, err := testDB.Exec(q); err != nil {
			t.Fatalf("purge cleanup: %v\nquery: %s", err, q)
		}
	}
}
