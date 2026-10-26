// Package plan resolves the account's effective rate-limit entitlement.
//
// The overview page and the key-detail page both display the same four
// dimensions. Resolving them in one place prevents the two pages from drift-
// ing: a change to how an expired trial falls back must not need two edits.
//
// Resolution order (PRD §3.2.1 + TDS §1.4 t_rate_limit_policy):
//  1. t_user.plan_id → t_plan;
//  2. NULL plan → FREE_DEFAULT;
//  3. expired TRIAL → FREE_DEFAULT (the row stays until a purchase replaces it);
//  4. the latest active USER-scope policy row wins (PLAN / TRIAL /
//     QUOTA_APPLY / OPS_OVERRIDE / FREE_DEFAULT). This is where an approved
//     提额 takes effect before the plan row is rewritten.
package plan

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Queryer is satisfied by *sql.DB and *sql.Tx.
type Queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Plan is the effective entitlement.
type Plan struct {
	Code            string
	Name            string
	RPM             int64
	TPM             int64
	Concurrency     int64
	DailyQuotaMicro *int64
	// Source names the row that supplied the four dimensions: PLAN / TRIAL /
	// QUOTA_APPLY / OPS_OVERRIDE / FREE_DEFAULT.
	Source string
	// TrialExpireAt is set only for a live TRIAL.
	TrialExpireAt *time.Time
}

const freeDefaultCode = "FREE_DEFAULT"

// Resolve reads the account's plan and overlays the active policy row.
func Resolve(ctx context.Context, q Queryer, uid int64, now time.Time) (Plan, error) {
	var (
		planID   sql.NullInt64
		trialEnd sql.NullTime
	)
	err := q.QueryRowContext(ctx, `
		SELECT plan_id, trial_expire_at
		  FROM t_user
		 WHERE id = ? AND deleted_at IS NULL`, uid).Scan(&planID, &trialEnd)
	if err != nil {
		return Plan{}, err
	}

	var p Plan
	if planID.Valid {
		err = q.QueryRowContext(ctx, `
			SELECT code, name, rpm, tpm, concurrency, daily_quota_micro
			  FROM t_plan WHERE id = ? AND enabled = 1`, planID.Int64).
			Scan(&p.Code, &p.Name, &p.RPM, &p.TPM, &p.Concurrency, &p.DailyQuotaMicro)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Plan{}, fmt.Errorf("plan: load plan %d: %w", planID.Int64, err)
		}
	}
	if p.Code == "" {
		if err := loadFreeDefault(ctx, q, &p); err != nil {
			return Plan{}, err
		}
	}
	if p.Code == "TRIAL" && trialEnd.Valid && !trialEnd.Time.After(now) {
		if err := loadFreeDefault(ctx, q, &p); err != nil {
			return Plan{}, err
		}
	}
	if p.Code == "TRIAL" && trialEnd.Valid {
		end := trialEnd.Time.UTC()
		p.TrialExpireAt = &end
	}
	p.Source = p.Code

	var (
		source string
		rpm    int64
		tpm    int64
		conc   int64
		daily  sql.NullInt64
	)
	err = q.QueryRowContext(ctx, `
		SELECT source, rpm, tpm, concurrency_quota, daily_quota_micro
		  FROM t_rate_limit_policy
		 WHERE scope_type = 'USER' AND scope_id = ?
		   AND effective_from <= ?
		   AND (effective_to IS NULL OR effective_to > ?)
		 ORDER BY effective_from DESC, id DESC
		 LIMIT 1`, uid, now, now).
		Scan(&source, &rpm, &tpm, &conc, &daily)
	if err == nil {
		p.Source = source
		p.RPM = rpm
		p.TPM = tpm
		p.Concurrency = conc
		p.DailyQuotaMicro = nullable(daily)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Plan{}, fmt.Errorf("plan: load active policy: %w", err)
	}
	return p, nil
}

func loadFreeDefault(ctx context.Context, q Queryer, p *Plan) error {
	err := q.QueryRowContext(ctx, `
		SELECT code, name, rpm, tpm, concurrency, daily_quota_micro
		  FROM t_plan WHERE code = ? AND enabled = 1`, freeDefaultCode).
		Scan(&p.Code, &p.Name, &p.RPM, &p.TPM, &p.Concurrency, &p.DailyQuotaMicro)
	if err != nil {
		return fmt.Errorf("plan: FREE_DEFAULT row is missing: %w", err)
	}
	return nil
}

func nullable(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	out := v.Int64
	return &out
}
