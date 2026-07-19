package healthcheck

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunReturnsZeroWhenHealthy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	if got := Run(portOf(t, ts.URL)); got != 0 {
		t.Fatalf("expected 0 for a healthy server, got %d", got)
	}
}

func TestRunReturnsOneWhenUnhealthy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	if got := Run(portOf(t, ts.URL)); got != 1 {
		t.Fatalf("expected 1 for an unhealthy server, got %d", got)
	}
}

func TestRunReturnsOneWhenUnreachable(t *testing.T) {
	// Port 1 is a reserved, privileged port nothing will be listening on
	// in a test environment, giving a reliable connection-refused case
	// without needing to coordinate with a real listener.
	if got := Run("1"); got != 1 {
		t.Fatalf("expected 1 for an unreachable server, got %d", got)
	}
}

func portOf(t *testing.T, rawURL string) string {
	t.Helper()
	parts := strings.Split(rawURL, ":")
	return parts[len(parts)-1]
}
