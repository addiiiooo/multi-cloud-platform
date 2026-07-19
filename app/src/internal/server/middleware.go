package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

type contextKey string

const requestIDKey contextKey = "requestID"

// requestIDMiddleware assigns a short random ID to every request, attaches
// it to the request's context so every log line emitted while handling
// that request can be correlated, and echoes it back as a response header
// so a client (or an engineer during an incident) can hand it back for
// support. crypto/rand is used instead of a UUID library specifically to
// avoid adding a dependency for something the standard library already
// does well enough for a correlation ID's purposes.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		w.Header().Set("X-Request-Id", id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

func newRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing means the system entropy source is broken —
		// exceptionally rare. Fall back to a timestamp so the request still
		// gets an identifier instead of the handler failing outright.
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// instrumentation records both the structured log line and the metrics
// observation for one request. Doing both in a single wrapper avoids
// wrapping the ResponseWriter twice for the same purpose.
func (s *Server) instrumentation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.metrics.IncInFlight()
		defer s.metrics.DecInFlight()

		start := time.Now()
		sw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(sw, r)

		duration := time.Since(start)

		// Metrics are keyed on a bounded set of known paths, not the raw
		// request path: without this, a client scanning many nonexistent
		// URLs would create one new label combination per unique path,
		// growing metrics.Registry's internal map without bound — an
		// unauthenticated memory-exhaustion vector. Logs intentionally
		// keep the raw path: seeing exactly what was requested (including
		// junk) is useful during an incident, and log volume is bounded by
		// shipping/rotation at the infrastructure layer instead of by
		// this process's own memory.
		s.metrics.Observe(r.Method, s.normalizeForMetrics(r.URL.Path), sw.status)
		s.logger.Info("request handled",
			slog.String("request_id", requestIDFromContext(r.Context())),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", sw.status),
			slog.Duration("duration", duration),
		)
	})
}

// normalizeForMetrics collapses any path pulse-api didn't register a route
// for into a single "unmatched" label, bounding the metrics cardinality
// regardless of how many distinct nonexistent paths a client requests.
func (s *Server) normalizeForMetrics(path string) string {
	if s.knownPaths[path] {
		return path
	}
	return "unmatched"
}

// recoverMiddleware ensures a panic in any handler becomes a logged 500
// response instead of crashing the process. One misbehaving request must
// never take down a pod that's serving many other concurrent requests.
func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Error("panic recovered",
					slog.Any("panic", rec),
					slog.String("path", r.URL.Path),
				)
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the status code a handler wrote, since
// http.ResponseWriter doesn't expose it directly and both logging and
// metrics need it.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusRecorder) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}
