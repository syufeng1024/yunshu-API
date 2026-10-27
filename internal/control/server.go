// Package control assembles the controld HTTP surface: /api/v1/auth/*,
// /api/console/* and /api/admin/*.
//
// Every route is registered through one helper that declares its permission.
// A handler cannot opt out of authorization by forgetting a menu entry: the
// permission is part of the route table in this file (T11 hard constraint 1).
//
// T12 routes are implemented in internal/control/{user,key,model}; this file
// only wires them so the permission column stays in one place.
package control

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/config"
	"yunshu.dev/core/internal/control/audit"
	"yunshu.dev/core/internal/control/auth"
	"yunshu.dev/core/internal/control/fund"
	"yunshu.dev/core/internal/control/key"
	"yunshu.dev/core/internal/control/model"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/control/ops"
	"yunshu.dev/core/internal/control/user"
	"yunshu.dev/core/internal/cryptobox"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
	"yunshu.dev/core/internal/redisx"
)

// Deps is the control plane's dependency set. controld owns the general main
// database and Redis pools (TDS §1).
type Deps struct {
	DB       *sql.DB
	Redis    *redisx.Client
	Sessions *mw.SessionManager
	Audit    *audit.Writer
	Logger   *obslog.Logger
	Now      clock.Clock

	// PublishRoutes republishes cfg:snap:ROUTING. The IP whitelist travels in
	// that snapshot, so a key created or edited with rules must trigger a
	// publish; P06 treats a missing snapshot entry as "no whitelist" (allow
	// all), which would make a fresh whitelist ineffective. Nil is allowed in
	// unit tests and degrades to the timer's four-minute refresh.
	PublishRoutes func(ctx context.Context) error

	// PublishPrices rebuilds K19 (standard price per model). nil is allowed in
	// unit tests; a committed price without a refresh answers 503
	// config_unavailable so the operator knows gatewayd still sees the old one.
	PublishPrices func(ctx context.Context) error

	// PaySecrets maps a payment channel (WECHAT / ALIPAY) to its callback
	// signing secret. It comes from the process environment: the 45 frozen
	// configuration keys carry no payment credential, and a secret stored in
	// t_config_item would be readable by every process that reads config.
	// A channel with no secret answers 503 and never skips verification.
	PaySecrets map[string]string

	// Mailer is the outbound e-mail seam. Nil is allowed: notifications fall
	// back to the in-app row plus a WARN.
	Mailer fund.Mailer

	// FingerprintKey is FP_HMAC_KEY from the environment (the YUNSHU_ prefix
	// is accepted as a fallback). Empty makes the trial claim answer 503
	// rather than hash device identifiers with no key.
	FingerprintKey string
}

// Server holds the route table and its dependencies.
type Server struct {
	d      Deps
	auth   *auth.Service
	user   *user.Service
	keys   *key.Service
	models *model.Service
	fund   *fund.Service
	ops    *ops.Service
}

// New validates the wiring and builds the server.
func New(d Deps) (*Server, error) {
	if d.DB == nil || d.Redis == nil || d.Sessions == nil {
		return nil, errors.New("control: server requires mysql, redis and a session manager")
	}
	if d.Now == nil {
		d.Now = clock.Real{}
	}
	if d.Audit == nil {
		d.Audit = &audit.Writer{DB: d.DB, Now: d.Now}
	}
	keys := key.New(d.DB, d.Redis, d.Now, d.Logger)
	keys.PublishRoutes = d.PublishRoutes
	funds := fund.New(d.DB, d.Redis, nil, d.Audit, d.Now, d.Logger)
	funds.PaySecrets = d.PaySecrets
	funds.PublishRoutes = d.PublishRoutes
	funds.Mailer = d.Mailer
	funds.FingerprintKey = d.FingerprintKey

	// CHANNEL_KEY_KEK is required only by the channel write path. A missing or
	// malformed KEK leaves Box nil: reads still work, and creating or rotating
	// an upstream key answers 503 config_unavailable instead of storing
	// plaintext or an envelope no node can open.
	box, boxErr := cryptobox.FromEnv()
	if boxErr != nil && d.Logger != nil {
		d.Logger.Warn(context.TODO(), "control: CHANNEL_KEY_KEK unavailable: "+boxErr.Error(),
			obslog.Fields{Stage: "control"})
	}
	opss := ops.New(d.DB, d.Audit, d.Now, d.Logger, d.PublishRoutes, box)
	opss.KeyAuth = redisx.NewKeyAuthCache(d.Redis)
	opss.PriceRefresh = d.PublishPrices
	opss.ConfigPublisher = &config.Publisher{
		DB:          d.DB,
		Redis:       config.NewGoRedis(d.Redis.Raw()),
		Now:         d.Now.Now,
		LockTimeout: 5 * time.Second,
	}
	return &Server{
		d: d,
		auth: &auth.Service{
			DB:       d.DB,
			Sessions: d.Sessions,
			Audit:    d.Audit,
			Logger:   d.Logger,
			Now:      d.Now,
		},
		user:   user.New(d.DB, d.Redis, d.Now, d.Logger),
		keys:   keys,
		models: model.New(d.DB, d.Now, d.Logger),
		fund:   funds,
		ops:    opss,
	}, nil
}

// Register installs every control-plane route. Each entry names its permission
// in one place; adding a route without a permission is a compile-time omission
// here, not a runtime surprise.
func (s *Server) Register(mux *http.ServeMux) {
	// Public: unauthenticated by design.
	mux.Handle("POST /api/v1/auth/register", s.public(s.auth.Register))
	mux.Handle("POST /api/v1/auth/login", s.public(s.auth.Login))

	// Authenticated, no capability required.
	mux.Handle("GET /api/v1/auth/me", s.authed("", s.auth.Me))
	mux.Handle("POST /api/v1/auth/logout", s.authed("", s.auth.Logout))

	// Console account.
	mux.Handle("GET /api/console/overview", s.authed(mw.PermUserRead, s.user.Overview))

	// Console keys: reads need key.read, mutations need key.write.
	mux.Handle("GET /api/console/keys", s.authed(mw.PermKeyRead, s.keys.List))
	mux.Handle("POST /api/console/keys", s.authed(mw.PermKeyWrite, s.keys.Create))
	mux.Handle("GET /api/console/keys/{id}", s.authed(mw.PermKeyRead, s.keys.Detail))
	mux.Handle("PATCH /api/console/keys/{id}", s.authed(mw.PermKeyWrite, s.keys.Update))
	mux.Handle("DELETE /api/console/keys/{id}", s.authed(mw.PermKeyWrite, s.keys.Delete))
	mux.Handle("GET /api/console/keys/{id}/audit", s.authed(mw.PermKeyRead, s.keys.ListAudit))
	mux.Handle("POST /api/console/keys/{id}/rotate", s.authed(mw.PermKeyWrite, s.keys.Rotate))

	// Console model catalog.
	mux.Handle("GET /api/console/models", s.authed(mw.PermModelRead, s.models.List))

	// Console fund: plans (read), orders (read/write), redeem (write).
	mux.Handle("GET /api/console/plans", s.authed(mw.PermFundRead, s.fund.Plans))
	mux.Handle("GET /api/console/orders", s.authed(mw.PermFundRead, s.fund.ListOrders))
	mux.Handle("POST /api/console/orders", s.authed(mw.PermFundWrite, s.fund.CreateOrder))
	mux.Handle("POST /api/console/redeem", s.authed(mw.PermFundWrite, s.fund.Redeem))

	// Console fund: refunds, invoices, quota applies and the trial claim.
	// Approval/payment transitions are exported methods for T16; the console
	// only submits and lists.
	mux.Handle("GET /api/console/refunds", s.authed(mw.PermFundRead, s.fund.ListRefunds))
	mux.Handle("POST /api/console/refunds", s.authed(mw.PermFundWrite, s.fund.CreateRefund))
	mux.Handle("GET /api/console/invoices", s.authed(mw.PermFundRead, s.fund.ListInvoices))
	mux.Handle("POST /api/console/invoices", s.authed(mw.PermFundWrite, s.fund.CreateInvoice))
	mux.Handle("GET /api/console/quota-applies", s.authed(mw.PermFundRead, s.fund.ListQuotaApplies))
	mux.Handle("POST /api/console/quota-applies", s.authed(mw.PermFundWrite, s.fund.CreateQuotaApply))
	mux.Handle("POST /api/console/trial/claim", s.authed(mw.PermFundWrite, s.fund.ClaimTrial))

	// Payment callback: public by design. The payment platform has no console
	// session; the HMAC signature over the body is the authentication, so this
	// route must never be wired through s.authed (no cookie, no sid).
	mux.Handle("POST /api/v1/pay/callback/{channel}", s.public(s.fund.Callback))

	// Admin: whoami stays behind admin.all. Business admin routes declare the
	// concrete permission instead: OPS may write channels/models, FINANCE may
	// read models, and neither gets admin.all.
	mux.Handle("GET /api/admin/whoami", s.authed(mw.PermAdminAll, s.handleAdminWhoami))

	// Operator channels. Reads use admin.read, not model.read: PERSONAL holds
	// model.read for the console catalog, and these views expose base_url and
	// key masks. Mutations need channel.write.
	mux.Handle("GET /api/admin/channels", s.authed(mw.PermAdminRead, s.ops.ListChannels))
	mux.Handle("GET /api/admin/channels/{id}", s.authed(mw.PermAdminRead, s.ops.GetChannel))
	mux.Handle("POST /api/admin/channels", s.authed(mw.PermChannelWrite, s.ops.CreateChannel))
	mux.Handle("PATCH /api/admin/channels/{id}", s.authed(mw.PermChannelWrite, s.ops.UpdateChannel))
	mux.Handle("POST /api/admin/channels/{id}/disable", s.authed(mw.PermChannelWrite, s.ops.DisableChannel))
	mux.Handle("DELETE /api/admin/channels/{id}", s.authed(mw.PermChannelWrite, s.ops.DeleteChannel))

	// Operator model master data. Reads use admin.read because this view
	// includes upstream cost prices; model.read stays the console catalog's
	// permission.
	mux.Handle("GET /api/admin/models", s.authed(mw.PermAdminRead, s.ops.ListModels))
	mux.Handle("GET /api/admin/models/{id}", s.authed(mw.PermAdminRead, s.ops.GetModel))
	mux.Handle("POST /api/admin/models", s.authed(mw.PermModelWrite, s.ops.CreateModel))
	mux.Handle("PATCH /api/admin/models/{id}", s.authed(mw.PermModelWrite, s.ops.UpdateModel))
	mux.Handle("POST /api/admin/models/{id}/listing-state", s.authed(mw.PermModelWrite, s.ops.SetListingState))
	mux.Handle("POST /api/admin/models/{id}/recommend-weight", s.authed(mw.PermModelWrite, s.ops.SetRecommendWeight))

	// T15 second segment: pricing, groups, announcements, risk, users and the
	// runtime config whitelist. Reads of /api/admin/users use admin.read rather
	// than user.read: PERSONAL holds user.read for its own profile, and this
	// list returns every account's disable reason.
	mux.Handle("GET /api/admin/prices", s.authed(mw.PermPriceWrite, s.ops.ListPrices))
	mux.Handle("POST /api/admin/prices", s.authed(mw.PermPriceWrite, s.ops.CreatePrice))
	mux.Handle("PATCH /api/admin/prices/{id}", s.authed(mw.PermPriceWrite, s.ops.UpdatePrice))

	mux.Handle("GET /api/admin/groups", s.authed(mw.PermGroupWrite, s.ops.ListGroups))
	mux.Handle("PATCH /api/admin/groups/{id}", s.authed(mw.PermGroupWrite, s.ops.UpdateGroup))

	mux.Handle("GET /api/admin/announcements", s.authed(mw.PermNoticeWrite, s.ops.ListAnnouncements))
	mux.Handle("POST /api/admin/announcements", s.authed(mw.PermNoticeWrite, s.ops.CreateAnnouncement))
	mux.Handle("PATCH /api/admin/announcements/{id}", s.authed(mw.PermNoticeWrite, s.ops.UpdateAnnouncement))
	mux.Handle("POST /api/admin/announcements/{id}/publish", s.authed(mw.PermNoticeWrite, s.ops.PublishAnnouncement))

	mux.Handle("GET /api/admin/risk/rules", s.authed(mw.PermRiskWrite, s.ops.ListRiskRules))
	mux.Handle("POST /api/admin/risk/rules", s.authed(mw.PermRiskWrite, s.ops.CreateRiskRule))
	mux.Handle("PATCH /api/admin/risk/rules/{id}", s.authed(mw.PermRiskWrite, s.ops.UpdateRiskRule))
	mux.Handle("DELETE /api/admin/risk/rules/{id}", s.authed(mw.PermRiskWrite, s.ops.DeleteRiskRule))
	mux.Handle("GET /api/admin/risk/hits", s.authed(mw.PermRiskWrite, s.ops.ListRiskHits))
	mux.Handle("POST /api/admin/risk/users/{id}/score", s.authed(mw.PermRiskWrite, s.ops.SetUserRiskScore))

	mux.Handle("GET /api/admin/users", s.authed(mw.PermAdminRead, s.ops.ListUsers))
	mux.Handle("GET /api/admin/users/{id}", s.authed(mw.PermAdminRead, s.ops.GetUser))
	mux.Handle("POST /api/admin/users/{id}/disable", s.authed(mw.PermUserWrite, s.ops.DisableUser))
	mux.Handle("POST /api/admin/users/{id}/enable", s.authed(mw.PermUserWrite, s.ops.EnableUser))

	mux.Handle("GET /api/admin/config", s.authed(mw.PermConfigWrite, s.ops.GetConfig))
	mux.Handle("PUT /api/admin/config", s.authed(mw.PermConfigWrite, s.ops.PutConfig))

	// Unknown /api paths return the shared error envelope instead of net/http's
	// plain-text 404.
	mux.Handle("/api/", s.public(func(w http.ResponseWriter, r *http.Request) {
		mw.WriteError(w, r, apierr.New(apierr.CodeUnknownEndpoint))
	}))
}

// public wraps a handler with panic recovery only.
func (s *Server) public(h http.HandlerFunc) http.Handler {
	return mw.Recover(s.d.Logger)(h)
}

// authed builds Recover → Session → Require(perm) → handler. An empty perm is
// the "logged in, no capability" case (me/logout).
func (s *Server) authed(perm mw.Permission, h http.HandlerFunc) http.Handler {
	var chain http.Handler = h
	if perm != "" {
		chain = mw.Require(perm)(chain)
	}
	chain = s.d.Sessions.Middleware()(chain)
	return mw.Recover(s.d.Logger)(chain)
}

// ---------------------------------------------------------------------------
// GET /api/admin/whoami — admin.all, read-only diagnostic.
// ---------------------------------------------------------------------------

type whoamiResponse struct {
	UID         string          `json:"uid"`
	Role        string          `json:"role"`
	Permissions []mw.Permission `json:"permissions"`
}

func (s *Server) handleAdminWhoami(w http.ResponseWriter, r *http.Request) {
	sess, ok := mw.SessionFrom(r.Context())
	if !ok {
		mw.WriteError(w, r, mw.SessionMissing())
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, whoamiResponse{
		UID:         strconv.FormatInt(sess.UID, 10),
		Role:        sess.Role,
		Permissions: mw.PermissionsFor(sess.Role),
	})
}
