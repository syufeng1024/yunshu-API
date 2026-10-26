package httpsrv

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"yunshu.dev/core/internal/obs/health"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
)

func TestDrainDoesNotCancelInflightRequest(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	var logs strings.Builder
	logger := obslog.NewWithWriter("gatewayd", "test", &logs, clock.Real{})

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	requestErr := make(chan error, 1)

	srv := New(Config{
		Svc:          "gatewayd",
		Env:          "test",
		Logger:       logger,
		Clock:        clock.Real{},
		Health:       health.New(),
		Listener:     ln,
		DrainTimeout: 3 * time.Second,
		Register: func(mux *http.ServeMux) {
			mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
				started <- struct{}{}
				<-release
				requestErr <- r.Context().Err()
				w.WriteHeader(http.StatusNoContent)
			})
		},
	})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	type result struct {
		resp *http.Response
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		req, reqErr := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+ln.Addr().String()+"/slow", nil)
		if reqErr != nil {
			resultCh <- result{err: reqErr}
			return
		}
		resp, reqErr := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		resultCh <- result{resp: resp, err: reqErr}
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("handler did not start")
	}

	cancel() // begin drain while the request is in flight

	select {
	case <-done:
		t.Fatal("Run returned before the in-flight request finished")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)

	res := <-resultCh
	if res.err != nil {
		t.Fatalf("request error: %v", res.err)
	}
	_, _ = io.Copy(io.Discard, res.resp.Body)
	res.resp.Body.Close()
	if res.resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", res.resp.StatusCode)
	}
	if err := <-requestErr; err != nil {
		t.Fatalf("in-flight request context was canceled during drain: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the in-flight request finished")
	}
}

func TestRunDrainsOnContextCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	var logs strings.Builder
	logger := obslog.NewWithWriter("controld", "test", &logs, clock.Real{})
	reg := health.New()
	reg.Add("config", func(context.Context) error { return nil })

	srv := New(Config{
		Svc:          "controld",
		Env:          "test",
		Logger:       logger,
		Clock:        clock.Real{},
		Health:       reg,
		Listener:     ln,
		DrainTimeout: 2 * time.Second,
		Register: func(mux *http.ServeMux) {
			mux.HandleFunc("/work", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
		},
	})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	client := &http.Client{Timeout: time.Second}
	baseURL := "http://" + ln.Addr().String()
	ready := false
	for i := 0; i < 100; i++ {
		req, reqErr := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/work", nil)
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		resp, reqErr := client.Do(req)
		if reqErr == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			ready = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		cancel()
		t.Fatal("server did not accept requests")
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil on clean drain", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within the drain budget")
	}

	if reg.Ready() {
		t.Error("health registry must be not-ready after drain starts")
	}
	out := logs.String()
	if !strings.Contains(out, "drain started") {
		t.Errorf("logs missing drain start: %s", out)
	}
	if !strings.Contains(out, "drain complete") {
		t.Errorf("logs missing drain complete: %s", out)
	}
	if strings.Count(strings.TrimSpace(out), "\n") == 0 {
		t.Log("single log line captured")
	}
}
