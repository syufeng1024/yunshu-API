// Command gatewayd is the stateless data-plane process.
//
// It owns the public API listener (:8080) and a separate metrics listener. It
// never opens a general main-database pool; the only main-database channel is
// the restricted gateway write DSN configured by GATEWAY_DB_MAX_CONNS.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"yunshu.dev/core/internal/obs/health"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
	"yunshu.dev/core/internal/platform/db"
	"yunshu.dev/core/internal/platform/env"
	"yunshu.dev/core/internal/platform/httpsrv"
	platformredis "yunshu.dev/core/internal/platform/redis"
)

const service = env.ServiceGateway

func main() {
	rootCtx := context.Background()

	cfg, err := env.Load(service)
	if err != nil {
		obslog.NewWithWriter(service, os.Getenv("ENV"), os.Stderr, clock.Real{}).
			Error(rootCtx, "startup failed: "+err.Error(), obslog.Fields{Stage: "startup", Status: 1})
		os.Exit(1)
	}

	logger := obslog.New(service, cfg.Env)
	logger.Info(rootCtx, "starting", obslog.Fields{Stage: "startup"})

	// The restricted gateway channel: 8 connections by default, 5min lifetime.
	gwDB, err := db.OpenGateway(cfg.GWWriteDSN, cfg.GatewayDBMaxConns)
	if err != nil {
		logger.Error(rootCtx, "open gateway db channel failed: "+err.Error(), obslog.Fields{Stage: "startup", Status: 1})
		os.Exit(1)
	}
	defer gwDB.Close()

	rdb := platformredis.Open(cfg.RedisAddr, cfg.RedisPassword, 50)
	defer rdb.Close()

	h := health.New()
	h.Add("redis", func(ctx context.Context) error {
		return platformredis.Ping(ctx, rdb)
	})
	h.Add("mysql", func(ctx context.Context) error {
		return db.Ping(ctx, gwDB)
	})
	h.Add("config", func(context.Context) error {
		// Configuration is validated synchronously in env.Load.
		return nil
	})

	api := httpsrv.New(httpsrv.Config{
		Addr:              cfg.GatewayAddr,
		Svc:               service,
		Env:               cfg.Env,
		Logger:            logger,
		Clock:             clock.Real{},
		Health:            h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // SSE responses must not be cut by a server-level deadline.
		IdleTimeout:       120 * time.Second,
	})

	metrics := httpsrv.New(httpsrv.Config{
		Addr:              cfg.MetricsAddr,
		Svc:               service,
		Env:               cfg.Env,
		Logger:            logger,
		Clock:             clock.Real{},
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
		Register: func(mux *http.ServeMux) {
			mux.Handle("GET /metrics", httpsrv.MetricsHandler(cfg.MetricsToken))
		},
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	installSignalHandling(rootCtx, logger, cancel)

	errCh := make(chan error, 2)
	go func() { errCh <- api.Run(ctx) }()
	go func() { errCh <- metrics.Run(ctx) }()

	var runErr error
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil && runErr == nil {
			runErr = err
			cancel()
		}
	}

	if runErr != nil {
		logger.Error(rootCtx, "server exited with error: "+runErr.Error(), obslog.Fields{Stage: "shutdown", Status: 1})
		os.Exit(1)
	}
	logger.Info(rootCtx, "shutdown complete", obslog.Fields{Stage: "shutdown", Status: 0})
}

// installSignalHandling drains on the first termination signal and exits
// immediately on the second, matching the 120s drain contract.
func installSignalHandling(rootCtx context.Context, logger *obslog.Logger, cancel context.CancelFunc) {
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Warn(rootCtx, fmt.Sprintf("signal received (%s); drain starts", sig), obslog.Fields{Stage: "drain"})
		cancel()

		sig = <-sigCh
		logger.Warn(rootCtx, fmt.Sprintf("second signal received (%s); forcing exit", sig), obslog.Fields{Stage: "drain", Status: 1})
		os.Exit(1)
	}()
}
