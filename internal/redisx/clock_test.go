package redisx

import (
	"context"
	"sync"
	"testing"
	"time"

	obslog "yunshu.dev/core/internal/obs/log"
)

type captureLogger struct {
	mu     sync.Mutex
	warns  int
	errors int
	msgs   []string
}

func (l *captureLogger) Warn(_ context.Context, msg string, _ obslog.Fields) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns++
	l.msgs = append(l.msgs, "WARN "+msg)
}

func (l *captureLogger) Error(_ context.Context, msg string, _ obslog.Fields) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors++
	l.msgs = append(l.msgs, "ERROR "+msg)
}

type captureObserver struct{ offsets []int64 }

func (o *captureObserver) ObserveClockOffset(offsetMS int64) { o.offsets = append(o.offsets, offsetMS) }

func TestApplySampleThresholds(t *testing.T) {
	logger := &captureLogger{}
	observer := &captureObserver{}
	clock := NewClock(logger, observer)
	ctx := context.TODO()

	if got := clock.applySample(ctx, 100_500, 100_000, 100_000); got != 500 {
		t.Fatalf("offset = %d want 500", got)
	}
	if logger.warns != 0 || logger.errors != 0 {
		t.Fatalf("500ms offset logged: %v", logger.msgs)
	}

	if got := clock.applySample(ctx, 101_500, 100_000, 100_000); got != 1_500 {
		t.Fatalf("offset = %d want 1500", got)
	}
	if logger.warns != 1 || logger.errors != 0 {
		t.Fatalf("1500ms offset: warns=%d errors=%d", logger.warns, logger.errors)
	}

	if got := clock.applySample(ctx, 106_000, 100_000, 100_000); got != 6_000 {
		t.Fatalf("offset = %d want 6000", got)
	}
	if logger.errors != 1 {
		t.Fatalf("6000ms offset did not log ERROR: %v", logger.msgs)
	}

	if len(observer.offsets) != 3 {
		t.Fatalf("observer saw %d offsets, want 3", len(observer.offsets))
	}
	if clock.OffsetMS() != 6_000 {
		t.Fatalf("stored offset = %d want 6000", clock.OffsetMS())
	}
}

func TestMinEpochUsesOffset(t *testing.T) {
	t0 := time.Date(2026, 10, 26, 12, 0, 30, 0, time.UTC)
	clock := NewStaticClock(t0)
	base := clock.MinEpoch()
	clock.offsetMS.Store(60_000)
	if got := clock.MinEpoch(); got != base+1 {
		t.Fatalf("MinEpoch with +60s offset = %d want %d", got, base+1)
	}
}

func TestStaticClockIsUTC(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	t0 := time.Date(2026, 10, 26, 23, 59, 0, 0, loc)
	clock := NewStaticClock(t0)
	if got := clock.Now(); !got.Equal(t0) {
		t.Fatalf("Now = %v want %v", got, t0)
	}
}
