// Package tests black-box tests the assembled service's HTTP surface over
// a real network listener, complementing the white-box unit tests
// colocated with each package under internal/. It lives in its own
// directory (a sibling of cmd/ and internal/, inside the src/ tree so it
// stays within Go's internal-package visibility boundary — see
// cmd/pulse-api/main.go's neighboring internal/ packages) specifically to
// exercise pulse-api the way a real client — or this milestone's own
// verification step, `curl localhost:8080/healthz` — actually does: as an
// opaque HTTP service, not as direct Go function calls.
package tests

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/adityasumanth/pulse-api/src/internal/config"
	"github.com/adityasumanth/pulse-api/src/internal/metrics"
	"github.com/adityasumanth/pulse-api/src/internal/server"
	"github.com/adityasumanth/pulse-api/src/internal/store"
)

func newRunningServer(t *testing.T) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{Port: "0", MaxPulses: 10}
	srv := server.New(cfg, logger, store.New(10), metrics.NewRegistry())
	ts := httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)
	return ts
}

func TestFullRequestLifecycle(t *testing.T) {
	ts := newRunningServer(t)
	client := ts.Client()

	resp, err := client.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /healthz, got %d", resp.StatusCode)
	}

	body, _ := json.Marshal(map[string]string{"message": "integration test pulse"})
	resp, err = client.Post(ts.URL+"/api/v1/pulses", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/v1/pulses failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	resp, err = client.Get(ts.URL + "/api/v1/pulses")
	if err != nil {
		t.Fatalf("GET /api/v1/pulses failed: %v", err)
	}
	defer resp.Body.Close()

	var pulses []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&pulses); err != nil {
		t.Fatalf("failed to decode pulses: %v", err)
	}
	if len(pulses) != 1 {
		t.Fatalf("expected 1 pulse, got %d", len(pulses))
	}
}
