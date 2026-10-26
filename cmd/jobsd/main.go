// Command jobsd is the background-task process.
//
// It exposes no HTTP port. T01 registers the task table and a leader-election
// placeholder; the actual leases, heartbeat, and task bodies land in T09.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
	"yunshu.dev/core/internal/platform/db"
	"yunshu.dev/core/internal/platform/env"
	platformredis "yunshu.dev/core/internal/platform/redis"
)

const service = env.ServiceJobs

type scheduledTask struct {
	Name   string
	Period string
}

// taskTable mirrors the job list frozen in the architecture document. The
// account daily-quota reset is intentionally absent: it is date-keyed, not a job.
var taskTable = []scheduledTask{
	{Name: "prehold_release", Period: "60s"},
	{Name: "order_reconcile", Period: "5m"},
	{Name: "daily_reconcile", Period: "daily 01:00"},
	{Name: "grant_expiry", Period: "daily 00:05"},
	{Name: "log_archive", Period: "daily 03:00"},
	{Name: "refund_poll", Period: "10m"},
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

	rdb := platformredis.Open(cfg.RedisAddr, cfg.RedisPassword, 20)
	defer rdb.Close()

	logger.Info(rootCtx, fmt.Sprintf("task table registered: count=%d", len(taskTable)), obslog.Fields{Stage: "startup"})
	for _, task := range taskTable {
		logger.Info(rootCtx, fmt.Sprintf("task registered: name=%s period=%s", task.Name, task.Period), obslog.Fields{Stage: "startup"})
	}
	logger.Warn(rootCtx, "leader election placeholder; tasks disabled until T09", obslog.Fields{Stage: "startup"})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	installSignalHandling(rootCtx, logger, cancel)

	<-ctx.Done()
	logger.Warn(rootCtx, "drain started, timeout=120s", obslog.Fields{Stage: "drain"})
	logger.Info(rootCtx, "drain complete", obslog.Fields{Stage: "drain", Status: 0})
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
