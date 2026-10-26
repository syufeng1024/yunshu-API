// Command gatewayd is the stateless data-plane process.
//
// It owns the public API listener (GATEWAY_HTTP_ADDR) and a separate metrics
// listener. It never opens a general main-database pool; the only main-database
// channel is the restricted gateway write DSN configured by
// GATEWAY_DB_MAX_CONNS (8 connections, 5min lifetime, 200ms prehold txn).
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"yunshu.dev/core/internal/balance"
	"yunshu.dev/core/internal/breaker"
	"yunshu.dev/core/internal/config"
	"yunshu.dev/core/internal/cryptobox"
	"yunshu.dev/core/internal/dispatch"
	"yunshu.dev/core/internal/gw"
	"yunshu.dev/core/internal/idemstore"
	"yunshu.dev/core/internal/ledger"
	"yunshu.dev/core/internal/obs/health"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
	"yunshu.dev/core/internal/platform/db"
	"yunshu.dev/core/internal/platform/env"
	"yunshu.dev/core/internal/platform/httpsrv"
	"yunshu.dev/core/internal/ratelimit"
	"yunshu.dev/core/internal/redisx"
	"yunshu.dev/core/internal/settle"
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

	// Configuration gate (T05): an unregistered strategy must stop the process
	// before the listener starts, not surface as a per-request fallback.
	if err := config.StartupCheck(rootCtx, gwDB); err != nil {
		logger.Error(rootCtx, "config startup check failed: "+err.Error(), obslog.Fields{Stage: "startup", Status: 1})
		os.Exit(1)
	}

	store := config.NewStore()
	cfgRedis := config.NewGoRedis(rdb.Raw())
	cfgLogger := obsConfigLogger{logger: logger}
	loader := &config.Loader{Redis: cfgRedis, Store: store, Logger: cfgLogger}
	if err := loader.RefreshAll(rootCtx); err != nil {
		// Not fatal: the store carries built-in defaults and the watcher keeps
		// retrying. /readyz reports the dependency state to the load balancer.
		logger.Warn(rootCtx, "initial config snapshot unavailable: "+err.Error(), obslog.Fields{Stage: "startup"})
	}

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()

	watcher := &config.Watcher{
		Redis:  cfgRedis,
		Store:  store,
		Loader: loader,
		Logger: cfgLogger,
	}
	go func() {
		if err := watcher.Run(ctx); err != nil {
			logger.Error(ctx, "config watcher stopped: "+err.Error(), obslog.Fields{Stage: "startup", Status: 1})
		}
	}()

	redisClock := redisx.NewClock(logger, nil)
	stopClock := redisClock.StartSync(ctx, rdb, 30*time.Second)
	defer stopClock()

	nodeID := hostname()
	deg := ratelimit.NewDegrader(rdb, redisClock, nodeID, ratelimit.ModeOff, 200_000)
	limiter := ratelimit.New(rdb, redisClock, deg, nil, logger)

	idemOpts := idemstore.DefaultOptions()
	if v, ok := store.Int(config.KeyIdemLeaseTTLSeconds); ok {
		idemOpts.LeaseTTL = time.Duration(v) * time.Second
	}
	if v, ok := store.Int(config.KeyIdemLeaseRenewSeconds); ok {
		idemOpts.RenewEvery = time.Duration(v) * time.Second
	}
	if v, ok := store.Int(config.KeyIdemDuplicateWindowSeconds); ok {
		idemOpts.DuplicateWindow = time.Duration(v) * time.Second
	}

	mirror := balance.New(rdb, gw.DBAccountSource{DB: gwDB}, 60*time.Second, logger)
	brk := breaker.New(rdb, redisClock, logger)

	// T09: P26 enqueue, GR-03 backlog gate, P28/P29 asynchronous rows and
	// P30 metric accumulation. All of them use the existing restricted
	// gateway channel; none of them is allowed to fail the request.
	enqueuer := settle.NewEnqueuer(gwDB, clock.Real{})
	backlog := settle.NewBacklog(gwDB, clock.Real{})
	usageWriter := settle.NewUsageWriter(gwDB, logger, clock.Real{})
	metricAcc := settle.NewMetrics(rdb, logger, clock.Real{})

	// The writers outlive the HTTP drain: cancel() stops accepting work, and
	// the final flush must still happen after the last request settles.
	bgCtx, bgCancel := context.WithCancel(context.WithoutCancel(ctx))
	writerDone := make(chan struct{})
	metricDone := make(chan struct{})
	go func() { defer close(writerDone); _ = usageWriter.Run(bgCtx) }()
	go func() { defer close(metricDone); _ = metricAcc.Run(bgCtx) }()

	deps := &gw.Deps{
		Config:        store,
		Catalog:       gw.NewCatalog(store),
		Auth:          redisx.NewKeyAuthCache(rdb),
		Balance:       mirror,
		Prices:        &config.PriceCache{Redis: cfgRedis},
		Ledger:        ledger.NewStore(gwDB, clock.Real{}),
		Limiter:       limiter,
		Idem:          idemstore.New(rdb, redisClock, idemstore.NewMySQL(gwDB), idemOpts, logger),
		Breaker:       brk,
		Tokens:        gw.HeuristicCounter{},
		CounterReader: gw.RedisCounters{Client: rdb},
		Backpressure:  gw.NewBackpressure(logger),
		ConnGuard:     gw.NewConnGuard(logger),
		SettleBacklog: backlog,
		Logger:        logger,
		Clock:         redisClock,
		NodeID:        nodeID,
	}
	if err := deps.Validate(); err != nil {
		logger.Error(rootCtx, "gateway wiring incomplete: "+err.Error(), obslog.Fields{Stage: "startup", Status: 1})
		os.Exit(1)
	}

	// The channel KEK. A missing or malformed KEK does not stop the process:
	// every dispatch then answers 503 config_unavailable, which is visible and
	// never falls back to a plaintext key (T08 hard constraint 21).
	box, err := cryptobox.FromEnv()
	if err != nil {
		logger.Error(rootCtx, "channel key KEK unavailable: "+err.Error(), obslog.Fields{Stage: "startup", Status: 1})
		box = nil
	}

	dispatcher := dispatch.New(dispatch.Options{
		Catalog:        deps.Catalog,
		Tokens:         deps.Tokens,
		Ledger:         deps.Ledger,
		Balance:        deps.Balance,
		Config:         deps.Config,
		Logger:         logger,
		Idem:           deps.Idem,
		KEK:            box,
		Cooldown:       brk,
		SettleEnqueuer: enqueuer,
		UsageSink:      usageWriter.WriteUsage,
		MetricSink:     metricAcc.Add,
		AttemptSink: func(a dispatch.Attempt) {
			usageWriter.WriteAttempt(a.SettleRecord(time.Now().UTC()))
		},
	})
	handler := gw.NewHandler(deps, dispatcher)

	h := health.New()
	h.Add("redis", func(ctx context.Context) error {
		return rdb.Ping(ctx)
	})
	h.Add("mysql", func(ctx context.Context) error {
		return db.Ping(ctx, gwDB)
	})
	h.Add("config", func(context.Context) error {
		if store.Snapshot(config.NsUpstream) == nil {
			return fmt.Errorf("config snapshot missing")
		}
		return nil
	})

	drain := time.Duration(120_000) * time.Millisecond
	if v, ok := store.Int(config.KeyGwDrainDeadlineMs); ok && v > 0 {
		drain = time.Duration(v) * time.Millisecond
	}

	api := httpsrv.New(httpsrv.Config{
		Addr:              cfg.GatewayAddr,
		Svc:               service,
		Env:               cfg.Env,
		Logger:            logger,
		Clock:             clock.Real{},
		Health:            h,
		DrainTimeout:      drain,
		ReadHeaderTimeout: gw.TLSHandshakeBudget,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // SSE responses must not be cut by a server-level deadline.
		IdleTimeout:       120 * time.Second,
		Register: func(mux *http.ServeMux) {
			mux.Handle("/v1/", handler)
		},
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

	// Final flush of P28/P29 rows and P30 deltas. A slow database must not
	// hold the process open indefinitely; the rows are logs, not money.
	bgCancel()
	for _, done := range []chan struct{}{writerDone, metricDone} {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			logger.Warn(rootCtx, "async writer flush timeout", obslog.Fields{Stage: "shutdown"})
		}
	}

	if runErr != nil {
		logger.Error(rootCtx, "server exited with error: "+runErr.Error(), obslog.Fields{Stage: "shutdown", Status: 1})
		os.Exit(1)
	}
	logger.Info(rootCtx, "shutdown complete", obslog.Fields{Stage: "shutdown", Status: 0})
}

func hostname() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "gatewayd"
}

// obsConfigLogger adapts the structured logger to config's Warn-only surface.
type obsConfigLogger struct{ logger *obslog.Logger }

func (l obsConfigLogger) Warn(ctx context.Context, msg string) {
	l.logger.Warn(ctx, msg, obslog.Fields{Stage: "config"})
}

// installSignalHandling drains on the first termination signal and exits
// immediately on the second, matching the 120s drain contract (TDS §4.4).
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
