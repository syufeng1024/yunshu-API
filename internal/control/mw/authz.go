package mw

import (
	"net/http"
	"sort"
)

// Permission is one server-side capability. Every route declares exactly one;
// hiding a menu item is never a substitute (T11 hard constraint 1).
type Permission string

// T11 shipped three permissions. T12 adds the two read capabilities its
// console routes need, T13 adds the two fund capabilities; the rest of the
// role matrix (fund.approve / price.write / model.write / ticket.reply ...)
// remains T15's.
const (
	PermUserRead  Permission = "user.read"
	PermKeyRead   Permission = "key.read"
	PermKeyWrite  Permission = "key.write"
	PermModelRead Permission = "model.read"
	PermFundRead  Permission = "fund.read"
	PermFundWrite Permission = "fund.write"
	PermAdminAll  Permission = "admin.all"
)

// rolePermissions is the current projection. FINANCE and OPS both receive
// admin.all because the permission vocabulary has no finer admin split yet;
// T15 replaces this table with the 10-role matrix. Keeping the table here
// means a handler can never decide on its own.
//
// T13 grants fund.read / fund.write to PERSONAL (a user buys their own plan
// and redeems their own code) and to SUPER_ADMIN. OPS and FINANCE are left
// untouched on purpose: the finance-side matrix is T15's deliverable, and
// widening it here would pre-empt that review.
var rolePermissions = map[string]map[Permission]bool{
	"PERSONAL": {
		PermUserRead:  true,
		PermKeyRead:   true,
		PermKeyWrite:  true,
		PermModelRead: true,
		PermFundRead:  true,
		PermFundWrite: true,
	},
	"OPS": {
		PermModelRead: true,
		PermAdminAll:  true,
	},
	"FINANCE": {
		PermModelRead: true,
		PermAdminAll:  true,
	},
	"SUPER_ADMIN": {
		PermUserRead:  true,
		PermKeyRead:   true,
		PermKeyWrite:  true,
		PermModelRead: true,
		PermFundRead:  true,
		PermFundWrite: true,
		PermAdminAll:  true,
	},
}

// PermissionsFor returns the role's permissions in stable order. An unknown
// role has none: a typo in t_user.role must not grant access.
func PermissionsFor(role string) []Permission {
	set := rolePermissions[role]
	out := make([]Permission, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Allows reports whether role holds p.
func Allows(role string, p Permission) bool {
	return rolePermissions[role][p]
}

// Require enforces one permission. It expects SessionReader to have run
// already; a missing session is a 401, a role without the permission is a 403.
func Require(p Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sess, ok := SessionFrom(r.Context())
			if !ok || sess == nil {
				WriteError(w, r, SessionMissing())
				return
			}
			if !Allows(sess.Role, p) {
				WriteError(w, r, PermissionDenied())
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
