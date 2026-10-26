// Command jobsd is the background-task process.
//
// It exposes no HTTP port. T09's first segment delivers the task registry and
// the t_job_lock lease + K25 heartbeat runtime. Task bodies (reconciliation,
// expiry, retention) are the second segment; this process only registers and
// lists them, then holds their leases so a second instance cannot execute the
// same task concurrently.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"yunshu.dev/core/internal/jobs"
	"yunshu.dev/core/internal/ledger"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
	"yunshu.dev/core/internal/platform/db"
	"yunshu.dev/core/internal/platform/env"
	"yunshu.dev/core/internal/redisx"
	"yunshu.dev/core/internal/settle"
)

const service = env.ServiceJobs

// taskFailures mirrors the frozen job list in TDS §4.6. Every entry has a side
// effect, so every entry requires the leader lease. The account daily-quota
// reset is intentionally absent: it is date-keyed, not a job.
var taskTable = []jobs.Task{
	{Name: "prehold_release", Period: 60 * time.Second, RequiresLeader: true, Timeout: 5 * time.Minute},
	{Name: "order_reconcile", Period: 5 * time.Minute, RequiresLeader: true, Timeout: 2 * time.Minute},
	{Name: "daily_reconcile", Cron: "0 0 3 * * *", RequiresLeader: true, Timeout: 30 * time.Minute},
	{Name: "grant_expiry", Cron: "0 5 0 * * *", RequiresLeader: true, Timeout: 10 * time.Minute},
	{Name: "log_archive", Cron: "0 0 3 * * *", RequiresLeader: true, Timeout: 30 * time.Minute},
	{Name: "refund_poll", Period: 10 * time.Minute, RequiresLeader: true, Timeout: 2 * time.Minute},
}

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

	mainDB, err := db.OpenMain(cfg.DBDSN, db.JobsPoolMaxOpen, db.MainPoolMaxIdle)
	if err != nil {
		logger.Error(rootCtx, "open main db failed: "+err.Error(), obslog.Fields{Stage: "startup", Status: 1})
		os.Exit(1)
	}
	defer mainDB.Close()

	// K25 uses the shared key namespace, so jobsd must use the same client
	// wrapper as the gateway rather than a bare go-redis client.
	rdb := redisx.New(redisx.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword, PoolSize: 20})
	defer rdb.Close()

	registry := jobs.NewRegistry()
	for _, task := range taskTable {
		if err := registry.Register(task); err != nil {
			logger.Error(rootCtx, "task registration failed: "+err.Error(), obslog.Fields{Stage: "startup", Status: 1})
			os.Exit(1)
		}
	}
	logger.Info(rootCtx, fmt.Sprintf("task table registered: count=%d", registry.Len()), obslog.Fields{Stage: "startup"})
	for _, task := range registry.Sorted() {
		schedule := task.Period.String()
		if task.Cron != "" {
			schedule = task.Cron
		}
		logger.Info(rootCtx, fmt.Sprintf("task registered: name=%s schedule=%s leader=%t timeout=%s concurrency=%s",
			task.Name, schedule, task.RequiresLeader, task.Timeout, task.Concurrency), obslog.Fields{Stage: "startup"})
	}

	instanceID := hostname() + "-" + strconv.Itoa(os.Getpid())
	election := jobs.NewElection(mainDB, rdb, instanceID, logger, clock.Real{})
	stopHeartbeat := election.StartHeartbeat(rootCtx)
	defer stopHeartbeat()

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	installSignalHandling(rootCtx, logger, cancel)

	var wg sync.WaitGroup

	// P26 consumer. Every jobsd replica runs one worker: the claim uses
	// FOR UPDATE SKIP LOCKED and ledger.Settle is keyed by request_id, so two
	// replicas can never charge twice. Holding a leader lease here would make
	// settlement availability depend on the lease table.
	settleWorker := settle.NewWorker(mainDB, ledger.NewStore(mainDB, clock.Real{}), clock.Real{}, logger, instanceID)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := settleWorker.Run(ctx); err != nil {
			logger.Error(rootCtx, "settle worker stopped: "+err.Error(), obslog.Fields{Stage: "P26", Status: 1})
		}
	}()

	for _, task := range registry.List() {
		if !task.RequiresLeader {
			// A read-only task may run on every instance. T09 has none; the
			// branch exists so the invariant is explicit rather than implied.
			continue
		}
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			election.Keep(ctx, name)
		}(task.Name)
	}

	<-ctx.Done()
	logger.Warn(rootCtx, "drain started, timeout=120s", obslog.Fields{Stage: "drain"})

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		logger.Info(rootCtx, "drain complete", obslog.Fields{Stage: "drain", Status: 0})
	case <-time.After(120 * time.Second):
		logger.Error(rootCtx, "drain timeout; forcing exit", obslog.Fields{Stage: "drain", Status: 1})
		os.Exit(1)
	}
}

func hostname() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "jobsd"
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
