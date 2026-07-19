// Command pulse-api runs the HTTP server described in the platform
// architecture: a stateless service exposing /healthz, /readyz, /metrics,
// and a small JSON resource, with no dependency on any cloud service —
// that starts in a later milestone.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/adityasumanth/pulse-api/src/internal/config"
	"github.com/adityasumanth/pulse-api/src/internal/healthcheck"
	"github.com/adityasumanth/pulse-api/src/internal/logging"
	"github.com/adityasumanth/pulse-api/src/internal/metrics"
	"github.com/adityasumanth/pulse-api/src/internal/server"
	"github.com/adityasumanth/pulse-api/src/internal/store"
)

func main() {
	os.Exit(run())
}

func run() int {
	healthcheckMode := flag.Bool("healthcheck", false,
		"run a single check against a locally running instance's /healthz and exit "+
			"(used by the container's HEALTHCHECK instruction; see internal/healthcheck)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		// The structured slog logger doesn't exist yet — its own level
		// comes from this same Config — so this one fatal message uses
		// the standard library logger instead.
		log.Printf("invalid configuration: %v", err)
		return 1
	}

	// The -healthcheck path never starts the server: distroless images have
	// no shell and no curl, so the container's HEALTHCHECK instruction runs
	// this same binary with a flag instead of an external HTTP client.
	if *healthcheckMode {
		return healthcheck.Run(cfg.Port)
	}

	logger := logging.New(cfg.LogLevel)
	reg := metrics.NewRegistry()
	st := store.New(cfg.MaxPulses)
	srv := server.New(cfg, logger, st, reg)

	// Binding synchronously (rather than inside ListenAndServe, which does
	// this internally on its own goroutine) means a port conflict is
	// reported immediately and clearly, and it lets serve()'s behavior be
	// exercised deterministically in a test without needing a real OS
	// signal or a sleep-based race against server startup.
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		logger.Error("failed to bind listener", slog.Any("error", err))
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return serve(ctx, stop, logger, srv, ln, cfg.ShutdownTimeout)
}

// serve runs srv until either it fails to start or ctx is cancelled, then
// attempts a graceful shutdown bounded by shutdownTimeout, returning a
// process exit code. Separated from run() so this behavior can be tested
// with an ordinary cancellable context instead of real OS signals.
func serve(ctx context.Context, stop context.CancelFunc, logger *slog.Logger, srv *server.Server, ln net.Listener, shutdownTimeout time.Duration) int {
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("pulse-api starting", slog.String("addr", ln.Addr().String()))
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	select {
	case err := <-serverErr:
		if err != nil {
			logger.Error("server failed", slog.Any("error", err))
			return 1
		}
		return 0

	case <-ctx.Done():
		// Kubernetes sends SIGTERM before killing a container; this is what
		// lets a rolling update finish in-flight requests instead of
		// dropping them, satisfying the architecture's zero-downtime
		// deployment requirement (Section 8, maxUnavailable: 0).
		logger.Info("shutdown signal received, draining in-flight requests")
		stop() // restore default signal behavior so a second signal force-quits

		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed, forcing exit", slog.Any("error", err))
			return 1
		}
		logger.Info("shutdown complete")
		return 0
	}
}
