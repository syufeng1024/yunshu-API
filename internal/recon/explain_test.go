package recon

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A13: the reconciliation scans must be index-driven. The test seeds enough
// rows that the optimizer has a real choice, then asserts EXPLAIN never reports
// type=ALL for a table the query filters by index.
func TestDailyPlansUseIndexes(t *testing.T) {
	day := time.Date(2026, 7, 15, 0, 0, 0, 0, CST)
	dayStart, dayEnd := day, day.AddDate(0, 0, 1)

	cleanupExplainUsers(t)
	t.Cleanup(func() { cleanupExplainUsers(t) })

	now := time.Now().UTC()
	// 2,000 accounts with one ledger entry each. The R-01 shard below covers
	// 100 of them, so the range is 5% of the table.
	for base := 0; base < 2000; base += 500 {
		var b strings.Builder
		b.WriteString(`INSERT INTO t_credit_account
		  (user_id, balance_recharge_micro, balance_bonus_micro, balance_micro,
		   overdraft_limit_micro, version, created_at, updated_at) VALUES `)
		args := make([]any, 0, 500*4)
		for i := base; i < base+500; i++ {
			if i > base {
				b.WriteByte(',')
			}
			b.WriteString(`(?,0,0,0,500000,0,?,?)`)
			args = append(args, 920000+i, now, now)
		}
		execFixture(t, b.String(), args...)
	}
	for base := 0; base < 2000; base += 500 {
		var b strings.Builder
		b.WriteString(`INSERT INTO t_ledger_entry
		  (user_id, entry_no, account_type, direction, biz_type,
		   amount_bonus_micro, amount_recharge_micro, amount_total_micro,
		   balance_before_micro, balance_after_micro, idem_key, created_at) VALUES `)
		args := make([]any, 0, 500*8)
		for i := base; i < base+500; i++ {
			if i > base {
				b.WriteByte(',')
			}
			b.WriteString(`(?,?, 'RECHARGE','CREDIT','RECHARGE',0,0,0,0,0,?,?)`)
			args = append(args, 920000+i, entryNo("EX13", i), fmt.Sprintf("ex13:%d", i), now)
		}
		execFixture(t, b.String(), args...)
	}
	for base := 0; base < 2000; base += 500 {
		var b strings.Builder
		b.WriteString(`INSERT INTO t_usage_record
		  (request_id, user_id, api_key_id, api_key_name_snap, model_name_snap,
		   request_model_req, inbound_protocol, http_status, result,
		   consumed_micro, total_ms, created_at) VALUES `)
		args := make([]any, 0, 500*5)
		for i := base; i < base+500; i++ {
			if i > base {
				b.WriteByte(',')
			}
			b.WriteString(`(?,?,1,'explain','explain','explain','OPENAI_COMPAT',200,'SUCCESS',1,1,?)`)
			args = append(args, rid("ex13", i), 920000+i, dayStart.Add(12*time.Hour).UTC())
		}
		execFixture(t, b.String(), args...)
	}
	// 100 of 2,000 idempotency rows are expired: a narrow range.
	for base := 0; base < 2000; base += 500 {
		var b strings.Builder
		b.WriteString(`INSERT INTO t_idempotency_record
		  (scope, idem_key, user_id, api_key_id, request_id, state, created_at, expires_at) VALUES `)
		args := make([]any, 0, 500*6)
		for i := base; i < base+500; i++ {
			if i > base {
				b.WriteByte(',')
			}
			b.WriteString(`('CLIENT_REQUEST',?,1,1,?,'DONE',?,?)`)
			args = append(args, fmt.Sprintf("ex13:%d", i), rid("exid", i), now, now.Add(time.Hour))
			if i < 100 {
				args[len(args)-1] = now.Add(-time.Hour)
			}
		}
		execFixture(t, b.String(), args...)
	}
	execFixture(t,
		`INSERT INTO t_metric_model_daily
		   (stat_date, model_id, request_total, consumed_micro, cost_micro, cost_configured, gross_profit_micro)
		 VALUES (?, 920000, 1, 100, 50, 1, 50)`, day.Format("2006-01-02"))
	for i := 1; i < 800; i++ {
		execFixture(t,
			`INSERT INTO t_metric_model_daily
			   (stat_date, model_id, request_total, consumed_micro, cost_micro, cost_configured, gross_profit_micro)
			 VALUES ('2026-07-14', ?, 1, 100, 50, 1, 50)`, 920000+i)
	}
	execFixture(t,
		`INSERT INTO t_upstream_bill
		   (channel_id, bill_adapter, bill_period, bill_no, amount_micro, created_at)
		 VALUES (920001,'MANUAL_UPLOAD',?, 'bill-0', 1, ?)`,
		day.Format("2006-01-02"), now)
	for i := 1; i < 800; i++ {
		execFixture(t,
			`INSERT INTO t_upstream_bill
			   (channel_id, bill_adapter, bill_period, bill_no, amount_micro, created_at)
			 VALUES (920001,'MANUAL_UPLOAD','2026-07-14', ?, 1, ?)`,
			fmt.Sprintf("bill-%d", i), now)
	}

	queries := []struct {
		name string
		q    string
		args []any
	}{
		{
			name: "R01 balance vs ledger",
			q: `SELECT a.user_id, a.balance_micro, COALESCE(SUM(l.amount_total_micro),0)
			      FROM t_credit_account a
			      LEFT JOIN t_ledger_entry l ON l.user_id = a.user_id
			     WHERE a.user_id >= ? AND a.user_id < ?
			     GROUP BY a.user_id, a.balance_micro
			    HAVING a.balance_micro <> COALESCE(SUM(l.amount_total_micro),0)`,
			args: []any{920000, 920100},
		},
		{
			name: "R03 per-user ledger page",
			q: `SELECT id, request_id, balance_before_micro, balance_after_micro
			      FROM t_ledger_entry
			     WHERE user_id = ? AND id > ?
			     ORDER BY id ASC LIMIT 1000`,
			args: []any{920500, 0},
		},
		{
			name: "R05 usage by user and day",
			q: `SELECT COALESCE(SUM(consumed_micro),0)
			      FROM t_usage_record
			     WHERE user_id = ? AND created_at >= ? AND created_at < ?`,
			args: []any{920500, dayStart.UTC(), dayEnd.UTC()},
		},
		{
			name: "R07 expired idempotency",
			q:    `SELECT COUNT(*) FROM t_idempotency_record WHERE expires_at < UTC_TIMESTAMP(3)`,
			args: nil,
		},
		{
			name: "R08 model gross",
			q: `SELECT model_id, consumed_micro, cost_micro, gross_profit_micro
			      FROM t_metric_model_daily
			     WHERE stat_date = ? AND cost_configured = 1
			       AND gross_profit_micro <> consumed_micro - cost_micro`,
			args: []any{day.Format("2006-01-02")},
		},
		{
			name: "external bills by period",
			q: `SELECT COALESCE(request_id,''), COALESCE(upstream_trade_no,''), COALESCE(bill_no,''),
			          channel_id, COALESCE(model_name,''), COALESCE(tokens_in,0),
			          COALESCE(tokens_out,0), amount_micro
			      FROM t_upstream_bill WHERE bill_period IN (?, ?)`,
			args: []any{day.Format("2006-01-02"), "20260715"},
		},
	}

	for _, qc := range queries {
		t.Run(qc.name, func(t *testing.T) {
			plan := explain(t, qc.q, qc.args...)
			for _, row := range plan {
				if row.Type == "ALL" {
					t.Fatalf("%s: table %s uses type=ALL\nplan=%+v", qc.name, row.Table, plan)
				}
			}
			t.Logf("%s: %s", qc.name, formatPlan(plan))
		})
	}
}

type planRow struct {
	Table, Type, Key, Extra string
	Rows                    sql.NullInt64
}

func explain(t *testing.T, q string, args ...any) []planRow {
	t.Helper()
	rows, err := testDB.QueryContext(context.Background(), "EXPLAIN "+q, args...)
	if err != nil {
		t.Fatalf("explain: %v\nquery: %s", err, q)
	}
	defer rows.Close()
	var out []planRow
	for rows.Next() {
		var id int
		var selectType, table, partitions, typ, possible, key, keyLen, ref, filtered sql.NullString
		var extra sql.NullString
		var n sql.NullInt64
		if err := rows.Scan(&id, &selectType, &table, &partitions, &typ, &possible,
			&key, &keyLen, &ref, &n, &filtered, &extra); err != nil {
			t.Fatalf("scan explain: %v", err)
		}
		out = append(out, planRow{
			Table: table.String, Type: typ.String, Key: key.String, Rows: n, Extra: extra.String,
		})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("explain rows: %v", err)
	}
	return out
}

func formatPlan(plan []planRow) string {
	parts := make([]string, 0, len(plan))
	for _, p := range plan {
		parts = append(parts, fmt.Sprintf("%s(type=%s,key=%s,rows=%d)", p.Table, p.Type, p.Key, p.Rows.Int64))
	}
	return strings.Join(parts, " ")
}

func cleanupExplainUsers(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`DELETE FROM t_upstream_attempt WHERE user_id BETWEEN 920000 AND 921999`,
		`DELETE FROM t_usage_record WHERE user_id BETWEEN 920000 AND 921999`,
		`DELETE FROM t_settle_task WHERE user_id BETWEEN 920000 AND 921999`,
		`DELETE FROM t_prehold_hold WHERE user_id BETWEEN 920000 AND 921999`,
		`DELETE FROM t_ledger_entry WHERE user_id BETWEEN 920000 AND 921999`,
		`DELETE FROM t_credit_batch WHERE user_id BETWEEN 920000 AND 921999`,
		`DELETE FROM t_credit_account WHERE user_id BETWEEN 920000 AND 921999`,
		`DELETE FROM t_user_daily_quota WHERE user_id BETWEEN 920000 AND 921999`,
		`DELETE FROM t_idempotency_record WHERE idem_key LIKE 'ex13:%'`,
		`DELETE FROM t_metric_model_daily WHERE model_id BETWEEN 920000 AND 921999`,
		`DELETE FROM t_upstream_bill WHERE channel_id = 920001`,
	} {
		if _, err := testDB.ExecContext(ctx, q); err != nil {
			t.Fatalf("explain cleanup: %v\nquery: %s", err, q)
		}
	}
}
