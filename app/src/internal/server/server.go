// Package server wires pulse-api's HTTP routes, in-memory store, metrics
// registry, and structured logger into a single, runnable server.
package server

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/adityasumanth/pulse-api/src/internal/config"
	"github.com/adityasumanth/pulse-api/src/internal/metrics"
	"github.com/adityasumanth/pulse-api/src/internal/store"
)

// Server embeds *http.Server so callers get ListenAndServe/Serve/Shutdown
// for free on the same value that also exposes pulse-api-specific
// extension points like RegisterReadinessCheck. An earlier version of
// this package had New return a bare *http.Server, which quietly made
// RegisterReadinessCheck uncallable from any real caller (there was no way
// to obtain the *Server needed to call it) — exactly the kind of
// self-contradicting API a PR review should catch before a later
// milestone runs into it.
type Server struct {
	*http.Server

	logger            *slog.Logger
	store             *store.Store
	metrics           *metrics.Registry
	readinessCheckers map[string]Checker
	knownPaths        map[string]bool
}

// New builds the HTTP server.
func New(cfg config.Config, logger *slog.Logger, st *store.Store, reg *metrics.Registry) *Server {
	s := &Server{
		logger:            logger,
		store:             st,
		metrics:           reg,
		readinessCheckers: map[string]Checker{},
		knownPaths:        map[string]bool{},
	}

	mux := http.NewServeMux()
	s.handle(mux, "GET /healthz", s.handleHealthz)
	s.handle(mux, "GET /readyz", s.handleReadyz)
	s.handle(mux, "GET /metrics", reg.Handler().ServeHTTP)
	s.handle(mux, "GET /api/v1/pulses", s.handleListPulses)
	s.handle(mux, "POST /api/v1/pulses", s.handleCreatePulse)

	// Order matters: recoverMiddleware must be outermost so it catches a
	// panic anywhere below it, including in the other middleware.
	// requestIDMiddleware must run before instrumentation so the request ID
	// it assigns is already on the context when instrumentation logs it.
	handler := s.recoverMiddleware(requestIDMiddleware(s.instrumentation(mux)))

	s.Server = &http.Server{
		Addr:    cfg.Addr(),
		Handler: handler,
		// Set explicitly rather than left to fall back to ReadTimeout, so
		// this is unambiguous under scrutiny from tools like gosec (G112:
		// potential Slowloris attack) as well as from a human reviewer.
		ReadHeaderTimeout: cfg.ReadTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}
	return s
}

// handle registers a Go 1.22+ ServeMux pattern (e.g. "GET /healthz") and
// records its path portion in knownPaths. Every route must be registered
// through this method rather than by calling mux.HandleFunc directly, or
// it silently falls outside the protection normalizeForMetrics depends on
// (see middleware.go): an unrecognized path would then be recorded in
// Prometheus metrics under its raw, attacker-controlled value instead of a
// bounded label.
func (s *Server) handle(mux *http.ServeMux, pattern string, handler http.HandlerFunc) {
	mux.HandleFunc(pattern, handler)
	if _, path, found := strings.Cut(pattern, " "); found {
		s.knownPaths[path] = true
	} else {
		s.knownPaths[pattern] = true
	}
}

// RegisterReadinessCheck adds a named check that /readyz evaluates on
// every request. No checks are registered by this milestone — this method
// exists purely as an extension point so a later milestone (for example,
// "can this pod reach Secrets Manager") can extend readiness without
// changing this package's handler code. It must be called during startup
// wiring, before the server begins serving requests: the underlying map is
// not safe for concurrent read/write, and registering checks is a
// one-time startup activity, not a runtime one.
func (s *Server) RegisterReadinessCheck(name string, check Checker) {
	s.readinessCheckers[name] = check
}
