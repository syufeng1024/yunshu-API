package fund

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"yunshu.dev/core/internal/jsonx"
)

// Mailer is the outbound e-mail seam. The repository has no SMTP client yet,
// so control.Deps injects an implementation when one exists. A nil Mailer is
// not an error: the in-app notification is still written and a WARN records
// that e-mail was skipped (hard constraint 14).
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Notification event codes permitted by t_notification's comment (B-64).
const (
	eventInvoiceProgress = "INVOICE_PROGRESS"
	eventRefundArrived   = "REFUND_ARRIVED"
)

const (
	channelInbox = 1 << 0
	channelEmail = 1 << 1
)

// notificationRetentionDays is B-64 A: the inbox keeps a message 180 days.
const notificationRetentionDays = 180

// notificationBodyMax and notificationTitleMax mirror the varchar sizes. The
// cut is by rune because MySQL counts characters, not bytes.
const (
	notificationBodyMax  = 1024
	notificationTitleMax = 128
)

// notify writes one in-app notification and, when a Mailer is injected and the
// account has an address, the matching e-mail. Every failure is a WARN: the
// business transaction has already committed and must not be rolled back
// because a notification could not be delivered (hard constraint 14).
func (s *Service) notify(ctx context.Context, userID int64, eventCode, title, body string) {
	if userID <= 0 {
		return
	}
	title = truncateRunes(strings.TrimSpace(title), notificationTitleMax)
	body = truncateRunes(strings.TrimSpace(body), notificationBodyMax)
	if title == "" || body == "" {
		s.logWarn(ctx, "fund_notify_empty event="+eventCode)
		return
	}

	var email sql.NullString
	if err := s.DB.QueryRowContext(ctx,
		`SELECT email FROM t_user WHERE id = ? AND deleted_at IS NULL`, userID).
		Scan(&email); err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.logWarn(ctx, "fund_notify_email_lookup_failed: "+err.Error())
	}

	mask := channelInbox
	state := map[string]string{"inbox": "SENT"}
	if s.Mailer == nil {
		s.logWarn(ctx, "fund_notify_mailer_not_configured event="+eventCode)
	} else if !email.Valid || strings.TrimSpace(email.String) == "" {
		state["email"] = "SKIPPED:NO_ADDRESS"
	} else {
		mask |= channelEmail
		sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err := s.Mailer.Send(sendCtx, strings.TrimSpace(email.String), title, body)
		cancel()
		if err != nil {
			s.logWarn(ctx, "fund_notify_email_failed event="+eventCode+": "+err.Error())
			state["email"] = "FAILED:" + truncateRunes(err.Error(), 120)
		} else {
			state["email"] = "SENT"
		}
	}

	rawState, err := jsonx.Marshal(state)
	if err != nil {
		s.logWarn(ctx, "fund_notify_state_marshal_failed: "+err.Error())
		rawState = []byte(`{"inbox":"SENT"}`)
	}
	now := s.now()
	if _, err := s.DB.ExecContext(ctx, `
		INSERT INTO t_notification
			(user_id, event_code, title, body, channel_mask, send_state,
			 read_at, created_at, expired_at)
		VALUES (?,?,?,?,?,?,NULL,?,?)`,
		userID, eventCode, title, body, mask, rawState,
		now, now.AddDate(0, 0, notificationRetentionDays)); err != nil {
		s.logWarn(ctx, "fund_notify_insert_failed event="+eventCode+": "+err.Error())
	}
}

// truncateRunes cuts s to at most n runes. The column widths are characters,
// so byte slicing could split a multi-byte character or waste half the row.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
