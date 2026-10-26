package mw

import (
	"net/http"
	"sort"
)

// Permission is one server-side capability. Every route declares exactly one;
// hiding a menu item is never a substitute (T11 hard constraint 1).
type Permission string

// The first segment ships three permissions only. The full role matrix
// (user.read / fund.approve / price.write / model.write / ticket.reply ...)
// belongs to the second segment.
const (
	PermKeyWrite  Permission = "key.write"
	PermModelRead Permission = "model.read"
	PermAdminAll  Permission = "admin.all"
)

// rolePermissions is the minimal first-segment projection. FINANCE and OPS
// both receive admin.all because the three-permission vocabulary has no finer
// admin split yet; the second segment replaces this table with the 10-role
// matrix. Keeping the table here means a handler can never decide on its own.
var rolePermissions = map[string]map[Permission]bool{
	"PERSONAL": {
		PermModelRead: true,
		PermKeyWrite:  true,
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
		PermModelRead: true,
		PermKeyWrite:  true,
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
