package dispatch

import (
	"strings"
	"unicode"
)

// Error classes used by the retry predicate and t_upstream_attempt.
const (
	ClassNone           = ""
	ClassTimeout        = "TIMEOUT"
	ClassDNS            = "DNS"
	ClassTLS            = "TLS"
	ClassConnectRefused = "CONNECT_REFUSED"
	ClassHTTP4XX        = "HTTP_4XX"
	ClassHTTP5XX        = "HTTP_5XX"
	ClassMalformedBody  = "MALFORMED_BODY"
	ClassEmptyChoices   = "EMPTY_CHOICES"
	ClassClientAbort    = "CLIENT_ABORT"
)

// Attempt is the in-memory row for one upstream try. T09 persists it to
// t_upstream_attempt; T08 must not write the database and must not cache the
// response body (except the sanitized upstream_msg prefix).
type Attempt struct {
	RequestID       string
	AttemptIndex    int
	UserID          int64
	ModelID         int64
	RouteID         int64
	ChannelID       int64
	ChannelNameSnap string
	ProtocolCode    string
	UpstreamModel   string
	Phase           string
	Result          string
	HTTPStatus      int
	UpstreamCode    int
	UpstreamMsg     string
	IsRetryable     bool
	RetryDecision   string
	ConnectMS       int64
	FirstByteMS     int64
	TotalMS         int64
	TokensIn        int64
	TokensOut       int64
	BytesIn         int64
	BytesOut        int64
	ErrorClass      string
}

// Retry decision labels (t_upstream_attempt.retry_decision).
const (
	DecisionNoRetry       = "NO_RETRY"
	DecisionNextCandidate = "RETRY_NEXT_CANDIDATE"
	DecisionSameRoute     = "RETRY_SAME_ROUTE"
)

// maxUpstreamMsg bounds upstream_msg. It is also redacted of the channel key
// before it is stored.
const maxUpstreamMsg = 512

// sanitizeUpstreamMsg truncates and redacts. The caller passes the plaintext
// key that was used for this attempt; if the upstream echoed it, it must not
// reach the attempt row, the log or the client.
func sanitizeUpstreamMsg(raw, secret string) string {
	msg := strings.TrimSpace(raw)
	if secret != "" {
		msg = strings.ReplaceAll(msg, secret, "[redacted]")
	}
	// Strip control characters that would break a single-line log field.
	msg = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, msg)
	if len(msg) > maxUpstreamMsg {
		msg = msg[:maxUpstreamMsg]
	}
	return msg
}
