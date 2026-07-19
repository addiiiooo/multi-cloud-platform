package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/adityasumanth/pulse-api/src/internal/config"
	"github.com/adityasumanth/pulse-api/src/internal/metrics"
	"github.com/adityasumanth/pulse-api/src/internal/server"
	"github.com/adityasumanth/pulse-api/src/internal/store"
)

func TestServeShutsDownGracefullyOnContextCancel(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{ShutdownTimeout: 2 * time.Second}
	srv := server.New(cfg, logger, store.New(10), metrics.NewRegistry())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to bind test listener: %v", err)
	}

	// Cancelling before serve() is even called makes this test
	// deterministic: srv.Serve blocks in its accept loop and cannot send
	// to serverErr on its own, so serve's select is guaranteed to observe
	// ctx.Done() rather than racing a sleep against server startup.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan int, 1)
	go func() { done <- serve(ctx, cancel, logger, srv, ln, cfg.ShutdownTimeout) }()

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("expected clean shutdown to return 0, got %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not return after context cancellation")
	}
}

func TestServeReturnsNonZeroWhenListenerFails(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{ShutdownTimeout: time.Second}
	srv := server.New(cfg, logger, store.New(10), metrics.NewRegistry())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to bind test listener: %v", err)
	}
	ln.Close() // force srv.Serve(ln) to fail immediately with a non-ErrServerClosed error

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if code := serve(ctx, cancel, logger, srv, ln, cfg.ShutdownTimeout); code != 1 {
		t.Fatalf("expected exit code 1 for a failed listener, got %d", code)
	}
}
