package dispatch

import "yunshu.dev/core/internal/gw"

// modelLabel reads the catalog model name. The request body's model string is
// never a label: it is user input, while the catalog name is the business
// closed set (TDS §5.3).
func modelLabel(c *gw.Ctx) string {
	if c != nil && c.Model != nil && c.Model.ModelName != "" {
		return c.Model.ModelName
	}
	return "unknown"
}

// attemptOutcome maps the attempt row to a bounded label. The recorder maps
// anything outside this set to "other" as a second guard.
func attemptOutcome(a Attempt) string {
	if a.Result != "" {
		return a.Result
	}
	if a.ErrorClass == ClassClientAbort {
		return "CLIENT_ABORT"
	}
	if a.ErrorClass != "" {
		return "FAILED"
	}
	return "UNKNOWN"
}
