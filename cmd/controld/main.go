// Command controld is the stateless control-plane process.
//
// It owns the console/admin listener (:8081) and holds the general main-database
// and Redis pools. Business handlers arrive with later task cards; T01 registers
// only the route prefixes and the health endpoints.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"yunshu.dev/core/internal/boot"
	"yunshu.dev/core/internal/config"
	"yunshu.dev/core/internal/control"
	"yunshu.dev/core/internal/control/audit"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/obs/health"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
	"yunshu.dev/core/internal/platform/db"
	"yunshu.dev/core/internal/platform/env"
	"yunshu.dev/core/internal/platform/httpsrv"
	"yunshu.dev/core/internal/redisx"
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

	rdb := redisx.New(redisx.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		PoolSize: 50,
	})
	defer rdb.Close()
	if err := rdb.Ping(rootCtx); err != nil {
		logger.Error(rootCtx, "redis ping failed: "+err.Error(), obslog.Fields{Stage: "startup", Status: 1})
		os.Exit(1)
	}
	if err := rdb.CheckPolicy(rootCtx); err != nil {
		logger.Error(rootCtx, "redis policy rejected: "+err.Error(), obslog.Fields{Stage: "startup", Status: 1})
		os.Exit(1)
	}

	sessions, err := mw.NewSessionManager(mainDB, rdb, cfg.SessionSecret)
	if err != nil {
		logger.Error(rootCtx, "session manager: "+err.Error(), obslog.Fields{Stage: "startup", Status: 1})
		os.Exit(1)
	}
	auditWriter := &audit.Writer{DB: mainDB, Now: clock.Real{}}
	ctl, err := control.New(control.Deps{
		DB:       mainDB,
		Redis:    rdb,
		Sessions: sessions,
		Audit:    auditWriter,
		Logger:   logger,
		Now:      clock.Real{},
	})
	if err != nil {
		logger.Error(rootCtx, "control server: "+err.Error(), obslog.Fields{Stage: "startup", Status: 1})
		os.Exit(1)
	}

	h := health.New()
	h.Add("redis", func(ctx context.Context) error {
		return rdb.Ping(ctx)
	})
	h.Add("mysql", func(ctx context.Context) error {
		return db.Ping(ctx, mainDB)
	})
	h.Add("config", func(ctx context.Context) error {
		n, err := rdb.Raw().Exists(ctx, config.SnapshotRedisKey(config.NsRouting)).Result()
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("routing snapshot missing")
		}
		return nil
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()

	// The first-segment producer. T15 replaces this loop with an operator
	// endpoint; until then the caches must be rebuilt before their TTLs expire.
	cfgRedis := config.NewGoRedis(rdb.Raw())
	publisher := &config.Publisher{DB: mainDB, Redis: cfgRedis, Now: clock.Real{}.Now}
	caches := &boot.CacheWriter{
		DB:     mainDB,
		Prices: &config.PriceCache{Redis: cfgRedis},
		Keys:   redisx.NewKeyAuthCache(rdb),
		Now:    clock.Real{},
	}
	refresher := &boot.Refresher{
		Routes: &boot.RouteSnapshot{
			DB: mainDB, Publisher: publisher, Now: clock.Real{}, Logger: logger,
		},
		Caches: caches,
		Logger: logger,
	}
	go refresher.Run(ctx)

	srv := httpsrv.New(httpsrv.Config{
		Addr:              cfg.ControlAddr,
		Svc:               service,
		Env:               cfg.Env,
		Logger:            logger,
		Clock:             clock.Real{},
		Health:            h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		Register:          ctl.Register,
	})

	installSignalHandling(rootCtx, logger, cancel)

	if err := srv.Run(ctx); err != nil {
		logger.Error(rootCtx, "server exited with error: "+err.Error(), obslog.Fields{Stage: "shutdown", Status: 1})
		os.Exit(1)
	}
	logger.Info(rootCtx, "shutdown complete", obslog.Fields{Stage: "shutdown", Status: 0})
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
