package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"
)

const progressInterval = 30 * time.Second

func awaitProgress(ctx context.Context, label string, wait func(context.Context) error) error {
	stop := progress(label)
	defer stop()
	return wait(ctx)
}

// Heartbeats run off the request path and stop before the caller prints results.
// Short operations stay quiet; these are status messages, not timing samples.
func progress(label string) func() {
	return progressEvery(label, progressInterval, os.Stderr)
}

func progressEvery(label string, interval time.Duration, out io.Writer) func() {
	done, stopped := make(chan struct{}), make(chan struct{})
	start := time.Now()
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				fmt.Fprintf(out, "%s: still working (%s elapsed)\n", label, time.Since(start).Round(time.Second))
			}
		}
	}()
	return func() { close(done); <-stopped }
}
