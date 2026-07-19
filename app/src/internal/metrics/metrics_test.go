package metrics

import (
	"strings"
	"testing"
)

func TestObserveAggregatesByLabelSet(t *testing.T) {
	r := NewRegistry()
	r.Observe("GET", "/healthz", 200)
	r.Observe("GET", "/healthz", 200)
	r.Observe("POST", "/api/v1/pulses", 201)

	var sb strings.Builder
	r.write(&sb)
	out := sb.String()

	if !strings.Contains(out, `pulse_http_requests_total{method="GET",path="/healthz",status="200"} 2`) {
		t.Fatalf("expected aggregated GET /healthz count of 2, got:\n%s", out)
	}
	if !strings.Contains(out, `pulse_http_requests_total{method="POST",path="/api/v1/pulses",status="201"} 1`) {
		t.Fatalf("expected POST count of 1, got:\n%s", out)
	}
	if !strings.Contains(out, "pulse_uptime_seconds") {
		t.Fatalf("expected uptime gauge, got:\n%s", out)
	}
}

func TestInFlightGauge(t *testing.T) {
	r := NewRegistry()
	r.IncInFlight()
	r.IncInFlight()
	if got := r.inFlight.Load(); got != 2 {
		t.Fatalf("expected in-flight of 2, got %d", got)
	}
	r.DecInFlight()
	if got := r.inFlight.Load(); got != 1 {
		t.Fatalf("expected in-flight of 1, got %d", got)
	}
}
