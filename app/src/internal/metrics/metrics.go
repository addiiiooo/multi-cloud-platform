// Package metrics exposes pulse-api's request metrics in Prometheus text
// exposition format on /metrics.
//
// This is a small, dependency-free implementation rather than the standard
// github.com/prometheus/client_golang library. That is a deliberate,
// scope-bound choice for this milestone, not a recommendation to hand-roll
// metrics in general: pulse-api needs exactly one counter and one gauge
// today, and the full client library is a disproportionate dependency to
// add for that. All metrics state is isolated behind this package's small
// exported API specifically so swapping in the official library later —
// once real histogram/summary metrics are needed — is a one-package change
// with no impact on server/handlers.go.
package metrics

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Registry holds pulse-api's process metrics. The zero value is not
// usable; construct with NewRegistry.
type Registry struct {
	startTime time.Time

	mu       sync.Mutex
	counters map[string]int64 // key: "method|path|status"

	inFlight atomic.Int64
}

// NewRegistry creates an empty metrics registry. Call it once at startup
// and share the single instance across the whole process.
func NewRegistry() *Registry {
	return &Registry{
		startTime: time.Now(),
		counters:  make(map[string]int64),
	}
}

// Observe records one completed HTTP request.
func (r *Registry) Observe(method, path string, status int) {
	key := fmt.Sprintf("%s|%s|%d", method, path, status)
	r.mu.Lock()
	r.counters[key]++
	r.mu.Unlock()
}

// IncInFlight and DecInFlight track requests currently being handled.
func (r *Registry) IncInFlight() { r.inFlight.Add(1) }
func (r *Registry) DecInFlight() { r.inFlight.Add(-1) }

// Handler returns an http.Handler serving the current metrics in
// Prometheus text exposition format.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		r.write(w)
	})
}

func (r *Registry) write(w io.Writer) {
	r.mu.Lock()
	counts := make(map[string]int64, len(r.counters))
	keys := make([]string, 0, len(r.counters))
	for k, v := range r.counters {
		counts[k] = v
		keys = append(keys, k)
	}
	r.mu.Unlock()
	sort.Strings(keys) // stable output: easier to diff/scrape-test, and no cardinality concern at this size

	fmt.Fprintln(w, "# HELP pulse_http_requests_total Total HTTP requests processed.")
	fmt.Fprintln(w, "# TYPE pulse_http_requests_total counter")
	for _, k := range keys {
		parts := strings.SplitN(k, "|", 3)
		fmt.Fprintf(w, "pulse_http_requests_total{method=%q,path=%q,status=%q} %d\n",
			parts[0], parts[1], parts[2], counts[k])
	}

	fmt.Fprintln(w, "# HELP pulse_http_requests_in_flight Requests currently being handled.")
	fmt.Fprintln(w, "# TYPE pulse_http_requests_in_flight gauge")
	fmt.Fprintf(w, "pulse_http_requests_in_flight %d\n", r.inFlight.Load())

	fmt.Fprintln(w, "# HELP pulse_uptime_seconds Seconds since the process started.")
	fmt.Fprintln(w, "# TYPE pulse_uptime_seconds gauge")
	fmt.Fprintf(w, "pulse_uptime_seconds %.0f\n", time.Since(r.startTime).Seconds())
}
