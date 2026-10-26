package config

import (
	"context"
	"errors"
	"time"
)

// DefaultPollInterval is the AD-10 fallback cadence. Pub/Sub is not the only
// channel: a dropped subscriber must cost at most one interval of staleness.
const DefaultPollInterval = 30 * time.Second

// DefaultSubscribeRetry is how long the watcher waits before re-establishing
// a lost subscription. Polling continues independently of this loop.
const DefaultSubscribeRetry = 5 * time.Second

// Watcher combines cfg:invalidate notifications with a version poll. Neither
// channel alone is sufficient: Pub/Sub loses messages for offline
// subscribers, and polling alone would make the UI's "publish" feel laggy.
type Watcher struct {
	Redis  Redis
	Store  *Store
	Loader *Loader
	Logger Logger
	// PollInterval defaults to DefaultPollInterval.
	PollInterval time.Duration
	// SubscribeChannel defaults to RedisInvalidateChannel. Tests use a unique
	// channel to simulate a subscriber that never receives the message.
	SubscribeChannel string
	// SubscribeRetry defaults to DefaultSubscribeRetry.
	SubscribeRetry time.Duration
}

func (w *Watcher) loader() *Loader {
	if w.Loader != nil {
		return w.Loader
	}
	return &Loader{Redis: w.Redis, Store: w.Store, Logger: w.Logger}
}

// Run blocks until ctx is cancelled. Transient Redis and Pub/Sub failures are
// logged and retried; they never stop the poll loop or panic.
func (w *Watcher) Run(ctx context.Context) error {
	if w.Store == nil || w.Redis == nil {
		return errWatcherConfig
	}

	trigger := make(chan struct{}, 1)
	go w.subscribeLoop(ctx, trigger)

	w.poll(ctx)
	ticker := time.NewTicker(w.pollInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			w.poll(ctx)
		case <-trigger:
			w.poll(ctx)
		}
	}
}

func (w *Watcher) pollInterval() time.Duration {
	if w.PollInterval > 0 {
		return w.PollInterval
	}
	return DefaultPollInterval
}

func (w *Watcher) subscribeRetry() time.Duration {
	if w.SubscribeRetry > 0 {
		return w.SubscribeRetry
	}
	return DefaultSubscribeRetry
}

func (w *Watcher) logger() Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return nopLogger{}
}

// poll reads the version first. A matching version costs one Redis GET and
// avoids rebuilding snapshots that have not changed.
func (w *Watcher) poll(ctx context.Context) {
	l := w.loader()
	target, exists, err := l.readVersion(ctx)
	if err != nil {
		w.logger().Warn(ctx, "config_version_read_failed: "+err.Error())
		return
	}
	if !exists {
		return
	}
	if w.Store.GlobalVersion() == target {
		return
	}
	if err := l.RefreshAll(ctx); err != nil {
		// Loader logs the detailed cause; keep this line so the poll path is
		// visible in metrics-oriented log searches too.
		w.logger().Warn(ctx, "config_refresh_failed: "+err.Error())
	}
}

func (w *Watcher) subscribeLoop(ctx context.Context, trigger chan<- struct{}) {
	channel := w.SubscribeChannel
	if channel == "" {
		channel = RedisInvalidateChannel
	}
	for {
		if ctx.Err() != nil {
			return
		}
		sub, err := w.Redis.Subscribe(ctx, channel)
		if err != nil {
			w.logger().Warn(ctx, "config_subscribe_failed: "+err.Error())
			if !sleepContext(ctx, w.subscribeRetry()) {
				return
			}
			continue
		}
		w.drain(ctx, sub, trigger)
		_ = sub.Close()
		if ctx.Err() != nil {
			return
		}
		if !sleepContext(ctx, w.subscribeRetry()) {
			return
		}
	}
}

func (w *Watcher) drain(ctx context.Context, sub Subscription, trigger chan<- struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-sub.Messages():
			if !ok {
				return
			}
			select {
			case trigger <- struct{}{}:
			default:
			}
		}
	}
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

var errWatcherConfig = errors.New("config: watcher requires redis and store")
