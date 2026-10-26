package apierr

import (
	"strconv"
	"strings"
)

// renderHint is the only place hint text is produced. The metadata table owns
// the wording; this function owns the one interpolation token, {retry}, which
// is filled from the effective retry_after_seconds so the hint cannot claim a
// different wait than the field.
func renderHint(template string, retryAfterSeconds int) string {
	if template == "" {
		return "携 request_id 联系客服"
	}
	return strings.ReplaceAll(template, "{retry}", strconv.Itoa(retryAfterSeconds))
}
