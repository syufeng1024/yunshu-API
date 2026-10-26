package gw

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"yunshu.dev/core/internal/obs/log"
)

// TLSHandshakeBudget is the P01 TLS handshake cap. net/http derives its TLS
// handshake timeout from ReadHeaderTimeout, which the server sets to this
// value; the constant is named here so the contract is greppable.
const TLSHandshakeBudget = 5 * time.Second

// PerKeyConnFactor is the P01 per-key connection cap: concurrency policy × 2
// (TDS §3.1). Beyond the cap the connection is reset, not answered with 429 —
// 429 is the per-Key concurrency request limiter (P11), a different mechanism.
const PerKeyConnFactor = 2

// ConnGuard counts in-flight requests per API key and enforces the ×2 cap.
//
// The cap is applied after P05 identifies the key, because an HTTP connection
// carries no key until the request headers are parsed. The counter therefore
// tracks concurrent requests on the key rather than raw TCP sockets; that is
// the closest enforceable point in net/http and keeps the connection from
// being permanently pinned by a malicious client.
type ConnGuard struct {
	logger *log.Logger

	mu     sync.Mutex
	counts map[int64]int
}

// NewConnGuard builds an empty guard.
func NewConnGuard(logger *log.Logger) *ConnGuard {
	return &ConnGuard{logger: logger, counts: make(map[int64]int)}
}

// Acquire adds one request to keyID. It returns false when the cap is
// exceeded; the caller must then reset the connection and must NOT release.
func (g *ConnGuard) Acquire(ctx context.Context, keyID, concurrency int64) bool {
	if g == nil || keyID <= 0 {
		return true
	}
	limit := concurrency * PerKeyConnFactor
	if limit <= 0 {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.counts[keyID] >= int(limit) {
		if g.logger != nil {
			g.logger.Warn(ctx, "gateway_conn_limit_exceeded", log.Fields{Stage: "P01"})
		}
		return false
	}
	g.counts[keyID]++
	return true
}

// Release removes one request from keyID. It is safe to call once per
// successful Acquire.
func (g *ConnGuard) Release(keyID int64) {
	if g == nil || keyID <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.counts[keyID] <= 1 {
		delete(g.counts, keyID)
		return
	}
	g.counts[keyID]--
}

// Count reports the current in-flight requests for keyID.
func (g *ConnGuard) Count(keyID int64) int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.counts[keyID]
}

// ResetConnection closes the client connection with SO_LINGER=0, which sends
// an RST instead of a FIN. If the ResponseWriter cannot be hijacked (HTTP/2),
// aborting the handler closes the stream, which is the protocol's equivalent.
func ResetConnection(w http.ResponseWriter) {
	rc := http.NewResponseController(w)
	conn, _, err := rc.Hijack()
	if err != nil {
		panic(http.ErrAbortHandler)
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetLinger(0)
	}
	_ = conn.Close()
}
