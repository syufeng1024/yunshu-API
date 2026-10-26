package idemstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
)

// MySQL is the authoritative idempotency store. Store always reaches this
// adapter before Redis (D-06/T06 hard constraint 17): a failed write here fails
// the request, while a failed Redis write only costs a later DB round trip.
//
// Lease semantics:
//   - Begin inserts IN_FLIGHT with the caller's fence and lease. The unique key
//     uk_idem_key (scope, api_key_id, idem_key) is the arbiter, so two
//     concurrent requests on one key cannot both insert.
//   - Takeover is a CAS on the old fence. It only matches a FAILED row or an
//     IN_FLIGHT row whose lease has expired; the new fence rotates on every
//     successful acquisition.
//   - Complete / Renew match on fence + request_id, so a writer whose lease was
//     taken over gets ErrFenceInvalid instead of overwriting the new owner.
type MySQL struct {
	db        *sql.DB
	retention time.Duration
}

// DefaultRetention is the dedup window recorded in expires_at:
// created_at + 24h (TDS §3.1). Record carries no expires_at field, so the
// adapter derives it.
const DefaultRetention = 24 * time.Hour

// fenceTokenWidth is t_idempotency_record.fence_token char(36). A longer value
// is rejected before it reaches MySQL, whose strict mode would otherwise fail
// with a truncation error that does not name the offending input.
const fenceTokenWidth = 36

const (
	insertRecordSQL = `INSERT INTO t_idempotency_record
  (scope, idem_key, user_id, api_key_id, request_id, state, body_hash,
   fence_token, lease_expires_at, created_at, expires_at)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	selectRecordSQL = `SELECT scope, api_key_id, user_id, idem_key, body_hash,
       state, request_id, fence_token, lease_expires_at, created_at, response_body
  FROM t_idempotency_record
 WHERE scope = ? AND api_key_id = ? AND idem_key = ?`

	lockRecordSQL = `SELECT fence_token, request_id
  FROM t_idempotency_record
 WHERE scope = ? AND api_key_id = ? AND idem_key = ?
 FOR UPDATE`

	completeSQL = `UPDATE t_idempotency_record
   SET state = ?, response_body = ?, lease_expires_at = NULL
 WHERE scope = ? AND api_key_id = ? AND idem_key = ?`

	renewSQL = `UPDATE t_idempotency_record
   SET lease_expires_at = ?
 WHERE scope = ? AND api_key_id = ? AND idem_key = ?`

	// Takeover rotates the whole ownership tuple. UTC_TIMESTAMP(3) is used
	// instead of NOW(3) so the comparison does not depend on the session
	// time_zone: the datetime(3) columns store UTC (TDS §0), and a non-UTC
	// session would otherwise shift every lease-expiry decision.
	// database, which is the only clock shared by every gateway replica.
	// FAILED bypasses the expiry test: a failed attempt may be retried
	// immediately, even before its old lease would have run out.
	takeoverSQL = `UPDATE t_idempotency_record
   SET user_id = ?, request_id = ?, state = ?, body_hash = ?,
       fence_token = ?, lease_expires_at = ?, created_at = ?, expires_at = ?
 WHERE scope = ? AND api_key_id = ? AND idem_key = ?
   AND fence_token = ?
   AND state IN ('IN_FLIGHT', 'FAILED')
   AND (state = 'FAILED' OR lease_expires_at IS NULL OR lease_expires_at < UTC_TIMESTAMP(3))`
)

// NewMySQL wraps a pool. The caller owns the pool; the adapter never closes it.
func NewMySQL(db *sql.DB) *MySQL {
	return &MySQL{db: db, retention: DefaultRetention}
}

// Begin inserts IN_FLIGHT or returns the row that already owns the key.
func (m *MySQL) Begin(ctx context.Context, rec Record) (*Record, bool, error) {
	if err := validateNewRecord(rec); err != nil {
		return nil, false, err
	}
	created := rec.CreatedAt.UTC()
	_, err := m.db.ExecContext(ctx, insertRecordSQL,
		rec.Scope, rec.IdemKey, rec.UserID, rec.APIKeyID, rec.RequestID, string(rec.State),
		rec.BodyHash, rec.FenceToken, rec.LeaseExpiresAt.UTC(), created, created.Add(m.retention),
	)
	if err == nil {
		return nil, true, nil
	}
	if !isDuplicateKey(err) {
		return nil, false, fmt.Errorf("idemstore: insert idempotency record: %w", err)
	}
	// 1062 means a committed row already owns uk_idem_key. Re-read it instead
	// of guessing which fields the other writer used.
	existing, getErr := m.Get(ctx, rec.Scope, rec.APIKeyID, rec.IdemKey)
	if getErr != nil {
		return nil, false, fmt.Errorf("idemstore: read record after duplicate key: %w", getErr)
	}
	return existing, false, nil
}

// Get reads one row by the business unique key.
func (m *MySQL) Get(ctx context.Context, scope string, apiKeyID int64, idemKey string) (*Record, error) {
	row := m.db.QueryRowContext(ctx, selectRecordSQL, scope, apiKeyID, idemKey)
	rec, err := scanRecord(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRecordNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("idemstore: read idempotency record: %w", err)
	}
	return rec, nil
}

// Complete writes the terminal state and clears the lease. The fence and
// request_id are checked under the row lock, so a request whose lease was
// taken over cannot overwrite the new owner's result.
func (m *MySQL) Complete(ctx context.Context, rec Record, state State, body []byte) error {
	if state != StateDone && state != StateFailed {
		return fmt.Errorf("idemstore: Complete with non-terminal state %q", state)
	}
	if err := validateOwnership(rec); err != nil {
		return err
	}

	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("idemstore: begin complete transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := checkOwner(ctx, tx, rec); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, completeSQL, string(state), body,
		rec.Scope, rec.APIKeyID, rec.IdemKey); err != nil {
		return fmt.Errorf("idemstore: complete idempotency record: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("idemstore: commit complete: %w", err)
	}
	return nil
}

// Renew extends the lease recorded for the current fence. The interval is a
// caller concern (idem.lease_renew_seconds); this adapter only writes until.
func (m *MySQL) Renew(ctx context.Context, rec Record, until time.Time) error {
	if err := validateOwnership(rec); err != nil {
		return err
	}
	if until.IsZero() {
		return errors.New("idemstore: renew without a lease deadline")
	}

	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("idemstore: begin renew transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := checkOwner(ctx, tx, rec); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, renewSQL, until.UTC(),
		rec.Scope, rec.APIKeyID, rec.IdemKey); err != nil {
		return fmt.Errorf("idemstore: renew idempotency lease: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("idemstore: commit renew: %w", err)
	}
	return nil
}

// Takeover claims a FAILED or lease-expired row. A zero row count means the
// fence moved (another replica won) or the lease is still live; both are
// "not taken", not an error.
func (m *MySQL) Takeover(ctx context.Context, rec Record, oldFence string) (bool, error) {
	if err := validateNewRecord(rec); err != nil {
		return false, err
	}
	// An empty oldFence is allowed on purpose: a row written before 0020 has
	// fence_token='' and lease_expires_at=NULL. Rejecting it here would leave
	// that row permanently un-takeoverable (every retry would answer WAIT until
	// the 24h cleanup). New rows always carry a UUIDv7 fence, so '' can never
	// match a live lease.
	created := rec.CreatedAt.UTC()
	result, err := m.db.ExecContext(ctx, takeoverSQL,
		rec.UserID, rec.RequestID, string(rec.State), rec.BodyHash,
		rec.FenceToken, rec.LeaseExpiresAt.UTC(), created, created.Add(m.retention),
		rec.Scope, rec.APIKeyID, rec.IdemKey, oldFence,
	)
	if err != nil {
		return false, fmt.Errorf("idemstore: take over idempotency lease: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("idemstore: read takeover row count: %w", err)
	}
	return affected == 1, nil
}

// checkOwner locks the row and verifies that rec still owns the lease.
func checkOwner(ctx context.Context, tx *sql.Tx, rec Record) error {
	var fence, requestID string
	err := tx.QueryRowContext(ctx, lockRecordSQL,
		rec.Scope, rec.APIKeyID, rec.IdemKey).Scan(&fence, &requestID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrRecordNotFound
	}
	if err != nil {
		return fmt.Errorf("idemstore: lock idempotency record: %w", err)
	}
	if fence != rec.FenceToken || requestID != rec.RequestID {
		return ErrFenceInvalid
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRecord(row rowScanner) (*Record, error) {
	var (
		rec   Record
		state string
		lease sql.NullTime
		body  []byte
	)
	if err := row.Scan(&rec.Scope, &rec.APIKeyID, &rec.UserID, &rec.IdemKey, &rec.BodyHash,
		&state, &rec.RequestID, &rec.FenceToken, &lease, &rec.CreatedAt, &body); err != nil {
		return nil, err
	}
	rec.State = State(state)
	rec.CreatedAt = rec.CreatedAt.UTC()
	if lease.Valid {
		rec.LeaseExpiresAt = lease.Time.UTC()
	}
	if len(body) > 0 {
		rec.ResponseBody = body
	}
	return &rec, nil
}

func validateNewRecord(rec Record) error {
	if rec.Scope == "" || rec.IdemKey == "" {
		return errors.New("idemstore: scope and idem_key are required")
	}
	if rec.BodyHash == "" {
		return errors.New("idemstore: body_hash is required")
	}
	if err := validateFence(rec.FenceToken); err != nil {
		return err
	}
	if rec.CreatedAt.IsZero() || rec.LeaseExpiresAt.IsZero() {
		return errors.New("idemstore: created_at and lease_expires_at are required")
	}
	return nil
}

func validateOwnership(rec Record) error {
	if rec.Scope == "" || rec.IdemKey == "" {
		return errors.New("idemstore: scope and idem_key are required")
	}
	if rec.RequestID == "" {
		return errors.New("idemstore: request_id is required")
	}
	return validateFence(rec.FenceToken)
}

func validateFence(fence string) error {
	if fence == "" {
		return ErrFenceInvalid
	}
	if utf8.RuneCountInString(fence) > fenceTokenWidth {
		return fmt.Errorf("idemstore: fence_token is %d characters, column is char(%d)",
			utf8.RuneCountInString(fence), fenceTokenWidth)
	}
	return nil
}

func isDuplicateKey(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
