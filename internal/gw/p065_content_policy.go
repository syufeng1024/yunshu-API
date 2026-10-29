package gw

import (
	"context"
	"time"

	"yunshu.dev/core/internal/apierr"
	obslog "yunshu.dev/core/internal/obs/log"
)

// contentPolicyHint is the only hint this stage emits. PRD §10.1 ⑤ requires the
// hint to name the appeal channel, not just the code; /console/tickets is the
// user console's ticket centre (PRD §2.1 row 19, §5.9).
const contentPolicyHint = "修改请求内容后重新提交；对判定有异议，请在控制台 → 工单中心 /console/tickets 提交内容申诉"

// ContentPolicyStage performs P06.5.
//
// Placement: immediately after P05 authentication, inside the P05 slot and
// ahead of P06/P07/P08 and P08.5. TDS §8.4.1 fixes this from both ends:
//
//   - after P05: t_content_policy_hit.user_id / api_key_id are NOT NULL and the
//     appeal flow (internal/control/ticket/create.go) looks a hit up by
//     user_id, so an unauthenticated request has no owner to record;
//   - after P04: the check needs the decoded messages, not raw JSON bytes;
//   - before P09/P10: no prehold exists, so the refused request writes no
//     t_ledger_entry and no t_prehold_hold row and never reaches a channel.
//
// The card's "P04 之后、P05 之前" wording predates this table layout; see the
// T42 delivery report for the discrepancy.
type ContentPolicyStage struct{ d *Deps }

// NewContentPolicyStage builds P06.5.
func NewContentPolicyStage(d *Deps) *ContentPolicyStage { return &ContentPolicyStage{d: d} }

// Name implements Stage.
func (s *ContentPolicyStage) Name() string { return StageP065 }

// Run implements Stage.
func (s *ContentPolicyStage) Run(ctx context.Context, c *Ctx) error {
	if s == nil || s.d == nil || s.d.PolicyChecker == nil {
		// Explicitly not configured, not a permissive checker. Deps.WarnDisabled
		// reports the state once at startup.
		return nil
	}
	if c == nil || c.IR == nil || c.Auth == nil {
		return StageErr(StageP065, apierr.New(apierr.CodeInternalError))
	}

	text := policyScanText(c.IR)
	if text == "" {
		return nil
	}

	verdict, err := s.d.PolicyChecker.Check(ctx, text)
	if err != nil {
		// fail-open (T42 decision 3): a checker failure must not take the
		// gateway down. The keyword checker has no I/O error path; this branch
		// is the external adapter's timeout/non-2xx/bad-response path.
		reason := policyFailOpenReason(err)
		if s.d.Metrics != nil {
			s.d.Metrics.ContentPolicyFailOpen(reason)
		}
		s.logFailOpen(ctx, c, reason, err)
		return nil
	}
	if !verdict.Blocked {
		return nil
	}

	hit := PolicyHit{
		RequestID:      c.RequestID,
		UserID:         c.Auth.UserID,
		APIKeyID:       c.Auth.KeyID,
		ModelName:      c.IR.Model,
		RuleID:         verdict.RuleID,
		RuleVersion:    verdict.RuleVersion,
		Category:       verdict.Category,
		Severity:       verdict.Severity,
		MatchedExcerpt: verdict.MatchedExcerpt,
		SnippetSHA256:  verdict.SnippetSHA256,
		SnippetOffset:  verdict.SnippetOffset,
		ClientIP:       c.ClientIP,
		Action:         PolicyActionBlock,
		CreatedAt:      time.UnixMilli(s.d.ClockNowMS()).UTC(),
	}
	if s.d.PolicyHits == nil {
		s.log(ctx, c, obslog.LevelError, "content_policy_hit_store_missing: violation refused without evidence row")
	} else {
		// Detach so a client disconnect between the check and the insert cannot
		// drop the evidence; the write is short and bounded.
		writeCtx, cancel := detach(ctx, 2*time.Second)
		writeErr := s.d.PolicyHits.Record(writeCtx, hit)
		cancel()
		if writeErr != nil {
			// The decision is already final. A failed evidence write must not
			// let violating content through, so the request is still refused.
			s.log(ctx, c, obslog.LevelError, "content_policy_hit_write_failed: "+writeErr.Error())
		}
	}

	// retry_after_seconds is 0 by the code spec: retrying the same content
	// cannot succeed, and a non-zero value would make SDKs back off and replay.
	e := apierr.New(apierr.CodeContentPolicyViolation).WithHint(contentPolicyHint)
	return StageErr(StageP065, e)
}

// logFailOpen writes the one line an alert rules on. The reason field is a
// bounded token (policyFailOpenReason), while the message keeps the concrete
// transport/status detail for the operator. Status is left at zero: the
// request is not refused, so a "status":403 line would misreport the outcome.
func (s *ContentPolicyStage) logFailOpen(ctx context.Context, c *Ctx, reason string, err error) {
	if s.d == nil || s.d.Logger == nil {
		return
	}
	msg := "content_policy_check_failed_open"
	if err != nil {
		msg += ": " + err.Error()
	}
	s.d.Logger.Warn(ctx, msg, obslog.Fields{
		Stage:     StageP065,
		RequestID: c.RequestID,
		Reason:    reason,
	})
}

func (s *ContentPolicyStage) log(ctx context.Context, c *Ctx, level obslog.Level, msg string) {
	if s.d == nil || s.d.Logger == nil {
		return
	}
	fields := obslog.Fields{Stage: StageP065, RequestID: c.RequestID, Status: 403}
	switch level {
	case obslog.LevelError:
		s.d.Logger.Error(ctx, msg, fields)
	case obslog.LevelWarn:
		s.d.Logger.Warn(ctx, msg, fields)
	default:
		s.d.Logger.Info(ctx, msg, fields)
	}
}
