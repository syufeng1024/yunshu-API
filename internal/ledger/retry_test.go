package ledger

import (
	"errors"
	"fmt"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// TestIsRetryableTxError pins the classification that withTx relies on. A
// retryable error re-runs the whole transaction, so mis-classifying one is
// either a user-visible 503 (too strict) or a duplicate write attempt (too
// loose); both are worth a test that never needs MySQL.
func TestIsRetryableTxError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"deadlock", &mysql.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock"}, true},
		{"lock wait timeout", &mysql.MySQLError{Number: 1205, Message: "Lock wait timeout exceeded"}, true},
		{
			"deadlock wrapped by the insert path",
			fmt.Errorf("ledger: insert prehold: %w",
				&mysql.MySQLError{Number: 1213, Message: "Deadlock found"}),
			true,
		},
		{"duplicate key is idempotency, not a race", &mysql.MySQLError{Number: 1062}, false},
		{"plain error", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := isRetryableTxError(c.err); got != c.want {
			t.Errorf("%s: isRetryableTxError = %v, want %v", c.name, got, c.want)
		}
	}
}
