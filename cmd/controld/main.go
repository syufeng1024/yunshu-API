// Command controld is the stateless control-plane process.
//
// It owns the console/admin listener (:8081) and holds the general main-database
// and Redis pools. Business handlers arrive with later task cards; T01 registers
// only the route prefixes and the health endpoints.
package main

import (
	"context"
	"fmt"
	"io"
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

const service = env.ServiceControl

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

	mainDB, err := db.OpenMain(cfg.DBDSN, db.MainPoolMaxOpen, db.MainPoolMaxIdle)
	if err != nil {
		logger.Error(rootCtx, "open main db failed: "+err.Error(), obslog.Fields{Stage: "startup", Status: 1})
		os.Exit(1)
	}
	defer mainDB.Close()

	rdb := platformredis.Open(cfg.RedisAddr, cfg.RedisPassword, 50)
	defer rdb.Close()

	h := health.New()
	h.Add("redis", func(ctx context.Context) error {
		return platformredis.Ping(ctx, rdb)
	})
	h.Add("mysql", func(ctx context.Context) error {
		return db.Ping(ctx, mainDB)
	})
	h.Add("config", func(context.Context) error {
		return nil
	})

	srv := httpsrv.New(httpsrv.Config{
		Addr:              ":8081",
		Svc:               service,
		Env:               cfg.Env,
		Logger:            logger,
		Clock:             clock.Real{},
		Health:            h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		Register: func(mux *http.ServeMux) {
			mux.Handle("/console/", notImplemented("console"))
			mux.Handle("/admin/", notImplemented("admin"))
			mux.Handle("/api/v1/auth/", notImplemented("auth"))
		},
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	installSignalHandling(rootCtx, logger, cancel)

	if err := srv.Run(ctx); err != nil {
		logger.Error(rootCtx, "server exited with error: "+err.Error(), obslog.Fields{Stage: "shutdown", Status: 1})
		os.Exit(1)
	}
	logger.Info(rootCtx, "shutdown complete", obslog.Fields{Stage: "shutdown", Status: 0})
}

func notImplemented(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = io.WriteString(w, `{"status":"not_implemented","scope":"`+scope+`"}`)
	}
}

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
