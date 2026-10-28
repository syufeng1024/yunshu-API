// Package httpsrv assembles the standard-library HTTP server used by
// gatewayd, controld and the jobsd metrics listener: health endpoints, request
// middleware, timeouts, and drain.
package httpsrv

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"yunshu.dev/core/internal/obs/health"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
)

// DefaultDrainTimeout is the SIGTERM drain budget.
const DefaultDrainTimeout = 120 * time.Second

// Config describes one HTTP listener.
type Config struct {
	Addr         string
	Svc          string
	Env          string
	Logger       *obslog.Logger
	Clock        clock.Clock
	Health       *health.Registry // nil disables /healthz and /readyz
	Register     func(*http.ServeMux)
	DrainTimeout time.Duration

	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration // 0 disables the server-level write timeout (SSE)
	IdleTimeout       time.Duration

	// Listener is an optional test hook. When nil, Run binds Addr.
	Listener net.Listener
}

// Server owns one http.Server and its drain behavior.
type Server struct {
	cfg     Config
	httpSrv *http.Server
}

// New assembles the handler chain and server timeouts.
func New(cfg Config) *Server {
	if cfg.Clock == nil {
		cfg.Clock = clock.Real{}
	}
	if cfg.DrainTimeout <= 0 {
		cfg.DrainTimeout = DefaultDrainTimeout
	}

	mux := http.NewServeMux()
	if cfg.Health != nil {
		mux.HandleFunc("GET /healthz", cfg.Health.Healthz)
		mux.HandleFunc("GET /readyz", cfg.Health.Readyz)
	}
	if cfg.Register != nil {
		cfg.Register(mux)
	}

	handler := Chain(
		mux,
		RequestID(cfg.Clock),
		TraceID(),
		AccessLog(cfg.Logger, cfg.Clock),
		Recover(cfg.Logger),
	)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: durationOr(cfg.ReadHeaderTimeout, 5*time.Second),
		ReadTimeout:       durationOr(cfg.ReadTimeout, 30*time.Second),
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       durationOr(cfg.IdleTimeout, 120*time.Second),
		MaxHeaderBytes:    1 << 20,
	}
	return &Server{cfg: cfg, httpSrv: srv}
}

// Run listens, serves, and drains when ctx is canceled. It returns nil on a
// clean drain (exit code 0) and an error only when the listener fails.
func (s *Server) Run(ctx context.Context) error {
	ln := s.cfg.Listener
	if ln == nil {
		var err error
		ln, err = net.Listen("tcp", s.cfg.Addr)
		if err != nil {
			return fmt.Errorf("listen %s: %w", s.cfg.Addr, err)
		}
	}

	// Request contexts must outlive the shutdown signal: drain lets in-flight
	// requests finish. WithoutCancel keeps values but drops the cancellation.
	requestBase := context.WithoutCancel(ctx)
	s.httpSrv.BaseContext = func(net.Listener) context.Context { return requestBase }
	if s.cfg.Health != nil {
		s.cfg.Health.SetReady(true)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.httpSrv.Serve(ln)
	}()
	s.cfg.Logger.Info(ctx, fmt.Sprintf("http listening on %s", ln.Addr().String()), obslog.Fields{
		Stage: "startup",
	})

	select {
	case <-ctx.Done():
		if s.cfg.Health != nil {
			s.cfg.Health.SetReady(false)
		}
		s.cfg.Logger.Warn(ctx, fmt.Sprintf("drain started, timeout=%s", s.cfg.DrainTimeout), obslog.Fields{
			Stage: "drain",
		})

		drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.DrainTimeout)
		defer cancel()

		if err := s.httpSrv.Shutdown(drainCtx); err != nil {
			s.cfg.Logger.Warn(ctx, fmt.Sprintf("drain deadline exceeded after %s; forcing close", s.cfg.DrainTimeout), obslog.Fields{
				Stage: "drain",
			})
			_ = s.httpSrv.Close()
			return nil
		}

		s.cfg.Logger.Info(ctx, "drain complete", obslog.Fields{Stage: "drain"})
		return nil

	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func durationOr(v, def time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return def
}
