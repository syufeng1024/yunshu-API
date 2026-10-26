// Command fakeupstream is a development-only OpenAI-compatible upstream used
// by T08's end-to-end checks. It is not part of the production build path; no
// other command imports it.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"yunshu.dev/core/internal/fakeupstream"
)

func main() {
	addr := flag.String("addr", ":18080", "listen address")
	mode := flag.String("mode", "ok", "ok | 429 | 500-once | no-done | half-frame | vendor-frame | slow | no-usage")
	failFirst := flag.Int("fail-first", 1, "number of leading 500 responses in 500-once mode")
	retryAfter := flag.String("retry-after", "30", "Retry-After seconds on 429")
	apiKey := flag.String("api-key", os.Getenv("FAKE_UPSTREAM_KEY"), "required bearer key (empty disables the check)")
	slow := flag.Duration("slow-delay", 5*time.Second, "header delay in slow mode")
	flag.Parse()

	server := fakeupstream.New(fakeupstream.Options{
		Mode:       fakeupstream.Mode(*mode),
		FailFirst:  *failFirst,
		RetryAfter: *retryAfter,
		APIKey:     *apiKey,
		SlowDelay:  *slow,
		Usage:      true,
	})
	srv := &http.Server{
		Addr:              *addr,
		Handler:           server,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       30 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() {
		<-ctx.Done()
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = srv.Shutdown(shutdown)
	}()

	fmt.Fprintf(os.Stderr, "fakeupstream listening on %s mode=%s\n", *addr, *mode)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
