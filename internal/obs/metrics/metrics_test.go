package metrics

import (
	"context"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCounterConcurrentAddDoesNotLoseIncrements(t *testing.T) {
	reg := NewRegistry()
	c := reg.Counter("test_events_total", "test counter").With()

	const goroutines = 32
	const perGoroutine = 1000
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				c.Inc()
			}
		}()
	}
	wg.Wait()

	if got, want := c.Value(), int64(goroutines*perGoroutine); got != want {
		t.Fatalf("counter = %d, want %d", got, want)
	}
	if got := sampleValue(t, reg.Snapshot(), "test_events_total"); got != goroutines*perGoroutine {
		t.Fatalf("snapshot = %d, want %d", got, goroutines*perGoroutine)
	}
}

func TestCounterWindowIsDeltaAndSnapshotIsCumulative(t *testing.T) {
	reg := NewRegistry()
	c := reg.Counter("test_window_total", "test counter").With()

	c.Add(5)
	if got := sampleValue(t, reg.windowSnapshot(), "test_window_total"); got != 5 {
		t.Fatalf("first window = %d, want 5", got)
	}
	if got := sampleValue(t, reg.Snapshot(), "test_window_total"); got != 5 {
		t.Fatalf("cumulative after first = %d, want 5", got)
	}

	c.Add(3)
	if got := sampleValue(t, reg.windowSnapshot(), "test_window_total"); got != 3 {
		t.Fatalf("second window = %d, want 3", got)
	}
	if got := sampleValue(t, reg.Snapshot(), "test_window_total"); got != 8 {
		t.Fatalf("cumulative after second = %d, want 8", got)
	}

	// A quiet window exports zero, not the cumulative 8.
	if got := sampleValue(t, reg.windowSnapshot(), "test_window_total"); got != 0 {
		t.Fatalf("quiet window = %d, want 0", got)
	}
}

func TestGaugeWindowCarriesCurrentValue(t *testing.T) {
	reg := NewRegistry()
	g := reg.Gauge("test_gauge", "test gauge").With()
	g.Set(7)
	if got := sampleValue(t, reg.windowSnapshot(), "test_gauge"); got != 7 {
		t.Fatalf("window gauge = %d, want 7", got)
	}
	g.Set(-2)
	if got := sampleValue(t, reg.windowSnapshot(), "test_gauge"); got != -2 {
		t.Fatalf("window gauge = %d, want -2", got)
	}
}

func TestHistogramBucketsSumAndCount(t *testing.T) {
	reg := NewRegistry()
	h := reg.Histogram("test_duration_seconds", "test histogram",
		[]float64{0.01, 0.1, 1}, "kind").With("a")
	h.ObserveDuration(5 * time.Millisecond)
	h.ObserveDuration(50 * time.Millisecond)
	h.ObserveDuration(2 * time.Second)

	snap := reg.Snapshot()
	fam := familyByName(t, snap, "test_duration_seconds")
	if len(fam.Samples) != 1 {
		t.Fatalf("samples = %d, want 1", len(fam.Samples))
	}
	s := fam.Samples[0]
	wantBuckets := []uint64{1, 1, 0, 1}
	if len(s.Buckets) != len(wantBuckets) {
		t.Fatalf("buckets = %v, want %v", s.Buckets, wantBuckets)
	}
	for i := range wantBuckets {
		if s.Buckets[i] != wantBuckets[i] {
			t.Fatalf("buckets = %v, want %v", s.Buckets, wantBuckets)
		}
	}
	if s.Count != 3 {
		t.Fatalf("count = %d, want 3", s.Count)
	}
	if math.Abs(s.Sum-2.055) > 1e-9 {
		t.Fatalf("sum = %f, want 2.055", s.Sum)
	}

	text := string(reg.PrometheusText())
	for _, want := range []string{
		`test_duration_seconds_bucket{kind="a",le="0.01"} 1`,
		`test_duration_seconds_bucket{kind="a",le="0.1"} 2`,
		`test_duration_seconds_bucket{kind="a",le="1"} 2`,
		`test_duration_seconds_bucket{kind="a",le="+Inf"} 3`,
		`test_duration_seconds_sum{kind="a"} 2.055`,
		`test_duration_seconds_count{kind="a"} 3`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text missing %q:\n%s", want, text)
		}
	}
}

func TestSeriesCeilingRejectsNewLabels(t *testing.T) {
	reg := NewRegistryWithLimit(2)
	v := reg.Counter("test_card_total", "test counter", "id")
	v.With("1").Inc()
	v.With("2").Inc()
	third := v.With("3")
	third.Inc()

	if got := reg.SeriesCount(); got != 2 {
		t.Fatalf("series count = %d, want 2", got)
	}
	if got := reg.SeriesRejected(); got != 1 {
		t.Fatalf("series rejected = %d, want 1", got)
	}
	if got := third.Value(); got != 0 {
		t.Fatalf("rejected child value = %d, want 0", got)
	}
	text := string(reg.PrometheusText())
	if strings.Contains(text, `id="3"`) {
		t.Fatalf("rejected label appeared in the export:\n%s", text)
	}
	if !strings.Contains(text, "metrics_series_rejected_total 1") {
		t.Fatalf("rejection counter missing:\n%s", text)
	}
}

func TestWrongLabelCountIsRejected(t *testing.T) {
	reg := NewRegistry()
	v := reg.Counter("test_bad_total", "test counter", "a", "b")
	v.With("only-one").Inc()

	if got := reg.SeriesCount(); got != 0 {
		t.Fatalf("series count = %d, want 0", got)
	}
	text := string(reg.PrometheusText())
	if !strings.Contains(text, "metrics_invalid_labels_total 1") {
		t.Fatalf("invalid-label counter missing:\n%s", text)
	}
}

var (
	helpLineRE   = regexp.MustCompile(`^# HELP ([a-zA-Z_:][a-zA-Z0-9_:]*) .+$`)
	typeLineRE   = regexp.MustCompile(`^# TYPE ([a-zA-Z_:][a-zA-Z0-9_:]*) (counter|gauge|histogram)$`)
	sampleNameRE = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*(?:\{[^}]*\})?$`)
)

func TestTextFormatIsParseable(t *testing.T) {
	reg := NewRegistry()
	reg.Counter("test_quoted_total", "help with \\ and newline\n", "label").
		With(`a"b\c`).Inc()
	reg.Gauge("test_negative_gauge", "negative values are valid").With().Set(-3)
	h := reg.Histogram("test_hist_seconds", "histogram", []float64{0.5, 1}).With()
	h.ObserveDuration(250 * time.Millisecond)

	text := string(reg.PrometheusText())
	if !strings.Contains(text, `test_quoted_total{label="a\"b\\c"} 1`) {
		t.Fatalf("label escaping is wrong:\n%s", text)
	}
	if !strings.Contains(text, "# HELP test_quoted_total help with \\\\ and newline\\n") {
		t.Fatalf("help escaping is wrong:\n%s", text)
	}

	seenType := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "# HELP ") {
			if !helpLineRE.MatchString(line) {
				t.Fatalf("malformed HELP line %q", line)
			}
			continue
		}
		if strings.HasPrefix(line, "# TYPE ") {
			m := typeLineRE.FindStringSubmatch(line)
			if m == nil {
				t.Fatalf("malformed TYPE line %q", line)
			}
			seenType[m[1]] = m[2]
			continue
		}
		idx := strings.LastIndexByte(line, ' ')
		if idx < 0 {
			t.Fatalf("sample line has no value: %q", line)
		}
		name := line[:idx]
		if !sampleNameRE.MatchString(name) {
			t.Fatalf("malformed sample name %q", name)
		}
		if _, err := strconv.ParseFloat(line[idx+1:], 64); err != nil {
			t.Fatalf("malformed sample value in %q: %v", line, err)
		}
	}

	for name, want := range map[string]string{
		"test_quoted_total":             "counter",
		"test_negative_gauge":           "gauge",
		"test_hist_seconds":             "histogram",
		"metrics_series_rejected_total": "counter",
		"metrics_invalid_labels_total":  "counter",
	} {
		if seenType[name] != want {
			t.Fatalf("TYPE %s = %q, want %q", name, seenType[name], want)
		}
	}
}

func TestRunEmitsWindowDeltas(t *testing.T) {
	reg := NewRegistry()
	c := reg.Counter("test_run_total", "test counter").With()
	c.Add(2)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan Snapshot, 8)
	done := make(chan error, 1)
	go func() {
		done <- reg.Run(ctx, 20*time.Millisecond, func(s Snapshot) { ch <- s })
	}()

	waitCounter(t, ch, "test_run_total", 2)
	c.Add(3)
	waitCounter(t, ch, "test_run_total", 3)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	if got := c.Value(); got != 5 {
		t.Fatalf("cumulative counter was reset by Run: %d", got)
	}
}

func TestRunWithNilSinkReturns(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Run(context.Background(), time.Millisecond, nil); err != nil {
		t.Fatalf("Run with nil sink = %v", err)
	}
}

func sampleValue(t *testing.T, snap Snapshot, name string) int64 {
	t.Helper()
	fam := familyByName(t, snap, name)
	if len(fam.Samples) != 1 {
		t.Fatalf("%s samples = %d, want 1", name, len(fam.Samples))
	}
	return fam.Samples[0].Value
}

func familyByName(t *testing.T, snap Snapshot, name string) FamilySnapshot {
	t.Helper()
	for _, fam := range snap.Families {
		if fam.Name == name {
			return fam
		}
	}
	t.Fatalf("family %s not found", name)
	return FamilySnapshot{}
}

func waitCounter(t *testing.T, ch <-chan Snapshot, name string, want int64) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case snap := <-ch:
			for _, fam := range snap.Families {
				if fam.Name != name || len(fam.Samples) != 1 {
					continue
				}
				if fam.Samples[0].Value == want {
					return
				}
			}
		case <-deadline:
			t.Fatalf("window with %s=%d was not emitted", name, want)
		}
	}
}
