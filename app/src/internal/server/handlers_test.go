package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/adityasumanth/pulse-api/src/internal/config"
	"github.com/adityasumanth/pulse-api/src/internal/metrics"
	"github.com/adityasumanth/pulse-api/src/internal/store"
)

func newTestServer() *Server {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{Port: "0", MaxPulses: 10}
	return New(cfg, logger, store.New(10), metrics.NewRegistry())
}

func do(t *testing.T, srv *Server, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	return rec
}

func TestHealthzAlwaysReturns200(t *testing.T) {
	srv := newTestServer()
	rec := do(t, srv, http.MethodGet, "/healthz", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestReadyzWithNoCheckersRegisteredIsReady(t *testing.T) {
	srv := newTestServer()
	rec := do(t, srv, http.MethodGet, "/readyz", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestReadyzReportsFailingCheckerAs503(t *testing.T) {
	srv := newTestServer()
	srv.RegisterReadinessCheck("dependency", func() error {
		return errors.New("dependency unreachable")
	})

	rec := do(t, srv, http.MethodGet, "/readyz", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Status != "not ready" {
		t.Fatalf(`expected status "not ready", got %q`, resp.Status)
	}
	if resp.Checks["dependency"] != "dependency unreachable" {
		t.Fatalf("expected the failing checker's error in the response, got %+v", resp.Checks)
	}
}

func TestCreateAndListPulses(t *testing.T) {
	srv := newTestServer()

	body, _ := json.Marshal(map[string]string{"message": "hello"})
	created := do(t, srv, http.MethodPost, "/api/v1/pulses", body)
	if created.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", created.Code, created.Body.String())
	}
	if ct := created.Header().Get("Content-Type"); ct != contentTypeJSON {
		t.Errorf("expected Content-Type %q, got %q", contentTypeJSON, ct)
	}

	listed := do(t, srv, http.MethodGet, "/api/v1/pulses", nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", listed.Code)
	}

	var pulses []struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &pulses); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(pulses) != 1 || pulses[0].Message != "hello" {
		t.Fatalf("unexpected pulses list: %+v", pulses)
	}
}

func TestCreatePulseRejectsEmptyMessage(t *testing.T) {
	srv := newTestServer()
	body, _ := json.Marshal(map[string]string{"message": ""})
	rec := do(t, srv, http.MethodPost, "/api/v1/pulses", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestCreatePulseRejectsMalformedJSON(t *testing.T) {
	srv := newTestServer()
	rec := do(t, srv, http.MethodPost, "/api/v1/pulses", []byte("not json"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestCreatePulseRejectsTrailingData(t *testing.T) {
	srv := newTestServer()
	body := []byte(`{"message":"a"}{"message":"b"}`)
	rec := do(t, srv, http.MethodPost, "/api/v1/pulses", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for trailing data, got %d", rec.Code)
	}
}

func TestCreatePulseRejectsOversizedBody(t *testing.T) {
	srv := newTestServer()
	huge := make([]byte, maxRequestBodyBytes+1)
	for i := range huge {
		huge[i] = 'x'
	}
	body, _ := json.Marshal(map[string]string{"message": string(huge)})
	rec := do(t, srv, http.MethodPost, "/api/v1/pulses", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an oversized body, got %d", rec.Code)
	}
}

func TestPulsesRouteRejectsUnsupportedMethod(t *testing.T) {
	srv := newTestServer()
	rec := do(t, srv, http.MethodDelete, "/api/v1/pulses", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for DELETE on a GET/POST-only route, got %d", rec.Code)
	}
}

func TestMetricsEndpointReportsObservedRequests(t *testing.T) {
	srv := newTestServer()
	do(t, srv, http.MethodGet, "/healthz", nil)

	rec := do(t, srv, http.MethodGet, "/metrics", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("pulse_http_requests_total")) {
		t.Fatalf("expected metrics output to contain pulse_http_requests_total, got: %s", rec.Body.String())
	}
}

func TestMetricsNormalizesUnknownPathsToBoundLabelCardinality(t *testing.T) {
	srv := newTestServer()
	do(t, srv, http.MethodGet, "/this-path-does-not-exist", nil)
	do(t, srv, http.MethodGet, "/neither-does-this-one", nil)

	rec := do(t, srv, http.MethodGet, "/metrics", nil)
	body := rec.Body.String()
	if !bytes.Contains([]byte(body), []byte(`path="unmatched"`)) {
		t.Fatalf(`expected unmatched paths to be aggregated under path="unmatched", got: %s`, body)
	}
	if bytes.Contains([]byte(body), []byte("this-path-does-not-exist")) {
		t.Fatalf("expected the raw unmatched path NOT to appear as its own metrics label, got: %s", body)
	}
}
