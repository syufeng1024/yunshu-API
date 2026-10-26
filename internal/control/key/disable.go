package key

import (
	"strings"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
)

// statusTransition applies the enable/disable rules:
//
//   - the target state is case-insensitive ENABLED / DISABLED;
//   - disabling is destructive and requires confirm=true; enabling does not;
//   - a transition to the current state is an idempotent no-op (F6-4: the
//     second click after a successful disable must not fail).
//
// A nil status (field absent) means "leave the state alone".
func statusTransition(current string, rawStatus *string, confirm *bool) (next string, changed bool, apiErr *apierr.APIError) {
	if rawStatus == nil {
		return current, false, nil
	}
	next = strings.ToUpper(strings.TrimSpace(*rawStatus))
	if next != "ENABLED" && next != "DISABLED" {
		return "", false, mw.InvalidField("status")
	}
	if next == current {
		return current, false, nil
	}
	if next == "DISABLED" && (confirm == nil || !*confirm) {
		return "", false, mw.InvalidField("confirm")
	}
	return next, true, nil
}
