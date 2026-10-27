// Package ticket implements the T16 ticket centre: the account's create / list
// / detail / close surface and the operator reply / state / internal-note
// surface.
//
// Three rules shape every file here:
//
//   - The state machine has exactly three states: OPEN, RESPONDED, CLOSED.
//     There is deliberately no fourth, auto-closed state. PRD F21-2: the
//     4-hour countdown is display and alert only — auto-closing would turn
//     "answered on time" into "closed on time" and make the SLA meaningless.
//   - An account can only ever see its own tickets. A row belonging to someone
//     else answers resource_not_found exactly like an absent row, and a
//     request_id that is not the caller's answers the same code and the same
//     message as one that never existed.
//   - Every operator action (public reply, state change, internal note) writes
//     its own t_admin_audit_log row in the same transaction. They are never
//     merged: F21-3 asks for three records, and a merged row would lose which
//     action carried the internal note.
package ticket

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/audit"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/jsonx"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
)

// Ticket types — the four PRD §5.9 values. CONTENT_APPEAL is the one that
// derives its rule id and request_id from t_content_policy_hit instead of
// letting the user copy them.
const (
	TypeBilling       = "BILLING"
	TypeTech          = "TECH"
	TypeContentAppeal = "CONTENT_APPEAL"
	TypeOther         = "OTHER"
)

// The three states, and only three. There is no auto-closed constant here and
// there must never be one (hard constraint 1).
const (
	StateOpen      = "OPEN"
	StateResponded = "RESPONDED"
	StateClosed    = "CLOSED"
)

const (
	// firstResponseSLA is the 4-hour promise (PRD §5.9 / §9.2). It is written
	// into t_ticket.first_response_due_at at creation; overdue tickets stay OPEN.
	firstResponseSLA = 4 * time.Hour

	// pickerWindow is F19-1: the request_id picker lists only the account's
	// requests from the last 90 days, and creation validates the same window.
	pickerWindow = 90 * 24 * time.Hour

	// appealWindow is TDS §8.4.5: a content-policy decision may be appealed
	// within 30 days of the hit. It is shorter than the 180-day evidence
	// retention window on purpose.
	appealWindow = 30 * 24 * time.Hour

	maxTitleRunes       = 120 // t_ticket.title varchar(120)
	maxDescriptionRunes = 5000
	maxReplyRunes       = 5000
	maxSearchRunes      = 64
	maxReasonRunes      = 255 // t_admin_audit_log.reason varchar(255)
	ticketNoMaxAttempts = 5

	// content appeal actions in t_content_policy_hit.action.
	hitActionBlock          = "BLOCK"
	hitActionAppealPending  = "APPEAL_PENDING"
	hitActionAppealUpheld   = "APPEAL_UPHELD"
	hitActionAppealOverturn = "APPEAL_OVERTURNED"
)

// Mailer is the outbound e-mail seam. It is structurally identical to
// fund.Mailer, so control.Deps.Mailer is assigned without an adapter. A nil
// Mailer is not an error: the reply still commits and a WARN records the skip
// (F21-1 requires the e-mail, but a dead SMTP server must not roll back a
// reply the operator already sent).
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Service owns both ticket consoles.
type Service struct {
	DB     *sql.DB
	Audit  *audit.Writer
	Now    clock.Clock
	Logger *obslog.Logger

	Mailer Mailer
}

// New wires the service. A nil clock or audit writer falls back to production
// implementations so a missing dependency cannot silently skip the trail.
func New(db *sql.DB, auditWriter *audit.Writer, now clock.Clock, logger *obslog.Logger) *Service {
	if now == nil {
		now = clock.Real{}
	}
	if auditWriter == nil {
		auditWriter = &audit.Writer{DB: db, Now: now}
	}
	return &Service{DB: db, Audit: auditWriter, Now: now, Logger: logger}
}

func (s *Service) now() time.Time { return s.Now.Now().UTC() }

func (s *Service) logError(ctx context.Context, msg string) {
	if s.Logger != nil {
		s.Logger.Error(ctx, msg, obslog.Fields{Stage: "ticket", Status: http.StatusInternalServerError})
	}
}

func (s *Service) logWarn(ctx context.Context, msg string) {
	if s.Logger != nil {
		s.Logger.Warn(ctx, msg, obslog.Fields{Stage: "ticket"})
	}
}

// ---------------------------------------------------------------------------
// request plumbing
// ---------------------------------------------------------------------------

// decodeJSON is the bounded body reader for ticket writes. Description and
// reply are capped at 5,000 runes, which fits in 64 KiB even when every rune
// is three bytes.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) *apierr.APIError {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	payload, err := io.ReadAll(r.Body)
	if err != nil || len(strings.TrimSpace(string(payload))) == 0 {
		return mw.InvalidField("body")
	}
	if err := jsonx.Unmarshal(payload, dst); err != nil {
		return mw.InvalidField("body")
	}
	return nil
}

// decodeOptionalJSON accepts an empty body (close carries no fields) and
// otherwise behaves like decodeJSON.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) *apierr.APIError {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		return mw.InvalidField("body")
	}
	if len(strings.TrimSpace(string(payload))) == 0 {
		return nil
	}
	if err := jsonx.Unmarshal(payload, dst); err != nil {
		return mw.InvalidField("body")
	}
	return nil
}

// sessionUID returns the console session's user id or the 401 to write.
func sessionUID(r *http.Request) (int64, *apierr.APIError) {
	sess, ok := mw.SessionFrom(r.Context())
	if !ok || sess == nil {
		return 0, mw.SessionMissing()
	}
	return sess.UID, nil
}

// actor returns the operator id and role. Session middleware has already run,
// so a missing session is a wiring bug answered as 401.
func actor(r *http.Request) (int64, string, *apierr.APIError) {
	sess, ok := mw.SessionFrom(r.Context())
	if !ok || sess == nil {
		return 0, "", mw.SessionMissing()
	}
	return sess.UID, sess.Role, nil
}

// pathTicketID reads {id}. A non-numeric id answers resource_not_found rather
// than a validation error: the response must not distinguish "malformed id"
// from "someone else's id" (hard constraint 8).
func pathTicketID(r *http.Request) (int64, *apierr.APIError) {
	raw := strings.TrimSpace(r.PathValue("id"))
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, apierr.New(apierr.CodeResourceNotFound)
	}
	return id, nil
}

// ---------------------------------------------------------------------------
// text normalisation
// ---------------------------------------------------------------------------

// normalizeText cleans an inbound free-text field (CLAUDE.md §3.1): CRLF and
// CR become LF, control characters are dropped, and the result is trimmed.
// Title (multiline=false) has its whitespace runs collapsed to one space
// because t_ticket.title renders on a single line.
func normalizeText(s string, multiline bool) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' && multiline:
			b.WriteRune(r)
		case r == '\t' && multiline:
			b.WriteRune('\t')
		case r == '\n' || r == '\t':
			b.WriteRune(' ')
		case r < 0x20 || r == 0x7f:
			// Drop other C0 controls and DEL. They have no meaning in a
			// plain-text ticket and would corrupt one-line UI fields.
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if !multiline {
		out = strings.Join(strings.Fields(out), " ")
	}
	return out
}

// truncateRunes cuts s to at most n runes. MySQL counts characters, not bytes.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// ---------------------------------------------------------------------------
// identifiers
// ---------------------------------------------------------------------------

// ticketNo builds t_ticket.ticket_no: "TK" + yyyyMMddHHmmss + 4 random digits
// = exactly 20 characters (char(20)). Collisions are caught by uk_tk_no and
// retried by the caller.
func ticketNo(now time.Time, rand func(int) string) string {
	return "TK" + now.UTC().Format("20060102150405") + rand(4)
}

// randomDigits returns exactly n decimal digits from crypto/rand. A failure
// falls back to zeros; the unique key plus retry loop is the real collision
// guard, so the fallback cannot create a silent duplicate.
func randomDigits(n int) string {
	if n <= 0 {
		return ""
	}
	var raw [8]byte
	if _, err := crand.Read(raw[:]); err != nil {
		return strings.Repeat("0", n)
	}
	v := binary.BigEndian.Uint64(raw[:])
	mod := uint64(1)
	for i := 0; i < n; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", n, v%mod)
}

// isDuplicateKey reports a MySQL unique-key violation (1062).
func isDuplicateKey(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1062
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// auditEntry built by every operator action. result stays short: the column is
// varchar(24) after migration 0023.
func (s *Service) writeAudit(ctx context.Context, tx *sql.Tx, r *http.Request,
	action string, ticketID int64, beforeState, afterState string,
	isInternal, hasInternalNote bool, reason string) error {
	uid, role, apiErr := actor(r)
	if apiErr != nil {
		return errors.New("ticket: audit requires a session")
	}
	before, after, err := audit.MarshalBeforeAfter(
		map[string]any{"state": beforeState},
		map[string]any{
			"state":             afterState,
			"is_internal":       isInternal,
			"has_internal_note": hasInternalNote,
		})
	if err != nil {
		return err
	}
	return s.Audit.Insert(ctx, tx, audit.Entry{
		OperatorID:   uid,
		OperatorRole: role,
		OperatorIP:   audit.IPBytes(r),
		Module:       "TICKET",
		Action:       action,
		TargetType:   "t_ticket",
		TargetID:     itoa(ticketID),
		Before:       before,
		After:        after,
		Reason:       truncateRunes(strings.TrimSpace(reason), maxReasonRunes),
		Result:       "OK",
	})
}
