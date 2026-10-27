package mw

import (
	"net/http"
	"sort"
)

// Permission is one server-side capability. Every route declares exactly one;
// hiding a menu item is never a substitute (T11 hard constraint 1).
type Permission string

// The full T15 vocabulary. Names are 资源.动作; a write permission always means
// "create, update, disable and delete that resource".
//
// admin.all remains a super-admin shortcut for cross-cutting diagnostics such
// as /api/admin/whoami, but no business route may rely on it: each admin
// endpoint declares the concrete capability it needs so a future role split
// does not silently grant everything.
const (
	PermUserRead     Permission = "user.read"
	PermUserWrite    Permission = "user.write"
	PermKeyRead      Permission = "key.read"
	PermKeyWrite     Permission = "key.write"
	PermModelRead    Permission = "model.read"
	PermModelWrite   Permission = "model.write"
	PermPriceWrite   Permission = "price.write"
	PermGroupWrite   Permission = "group.write"
	PermChannelWrite Permission = "channel.write"
	PermNoticeWrite  Permission = "notice.write"
	PermRiskWrite    Permission = "risk.write"
	PermConfigWrite  Permission = "config.write"
	PermFundRead     Permission = "fund.read"
	PermFundWrite    Permission = "fund.write"
	PermFundApprove  Permission = "fund.approve"
	PermTicketReply  Permission = "ticket.reply"
	PermAuditRead    Permission = "audit.read"
	// admin.read gates the operator console's read routes. model.read cannot
	// do that job: PERSONAL holds model.read for /api/console/models, and the
	// admin model view carries upstream cost prices (B-48) and channel views
	// carry base_url and key masks. Letting model.read open those routes would
	// hand every customer the cost sheet.
	PermAdminRead Permission = "admin.read"
	PermAdminAll  Permission = "admin.all"
)

// rolePermissions is the T15 matrix, keyed by the PRD §6.3 role enum
// (PERSONAL / OPS / FINANCE / SUPER_ADMIN). T11/T15 cards mention a "10-role
// matrix", but PRD §6.3 enumerates exactly four; no other role name is
// implemented, because a name the product does not define would be dead code.
//
// The split that matters for money: OPS may edit prices and channels but not
// approve a refund; FINANCE may approve a refund but not edit prices or
// channels. SUPER_ADMIN is the only role holding admin.all.
//
// PERSONAL holds fund.read/fund.write because a user buys their own plan and
// redeems their own code; every fund handler still scopes by session uid.
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
		PermUserRead:     true,
		PermUserWrite:    true,
		PermModelRead:    true,
		PermModelWrite:   true,
		PermPriceWrite:   true,
		PermGroupWrite:   true,
		PermChannelWrite: true,
		PermNoticeWrite:  true,
		PermRiskWrite:    true,
		PermConfigWrite:  true,
		PermTicketReply:  true,
		PermAuditRead:    true,
		PermAdminRead:    true,
	},
	"FINANCE": {
		PermFundRead:    true,
		PermFundApprove: true,
		PermAuditRead:   true,
		PermModelRead:   true,
		PermAdminRead:   true,
	},
	"SUPER_ADMIN": {
		PermUserRead:     true,
		PermUserWrite:    true,
		PermKeyRead:      true,
		PermKeyWrite:     true,
		PermModelRead:    true,
		PermModelWrite:   true,
		PermPriceWrite:   true,
		PermGroupWrite:   true,
		PermChannelWrite: true,
		PermNoticeWrite:  true,
		PermRiskWrite:    true,
		PermConfigWrite:  true,
		PermFundRead:     true,
		PermFundWrite:    true,
		PermFundApprove:  true,
		PermTicketReply:  true,
		PermAuditRead:    true,
		PermAdminRead:    true,
		PermAdminAll:     true,
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
