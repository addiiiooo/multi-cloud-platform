package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
)

const (
	contentTypeJSON = "application/json; charset=utf-8"

	// maxRequestBodyBytes bounds how much of a request body
	// handleCreatePulse will read. Without an explicit limit, a client can
	// send an arbitrarily large body and the server will buffer all of it
	// while decoding, before the store's own message-length validation
	// ever gets a chance to reject it — an easy, unauthenticated memory
	// exhaustion vector. 1 MiB is generous for a small JSON message.
	maxRequestBodyBytes = 1 << 20
)

type statusResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

// handleHealthz answers Kubernetes' liveness probe. It reports on the
// process itself only — never on downstream dependencies — because a
// failing liveness probe restarts the container. Coupling liveness to a
// dependency (e.g., a downstream API being slow) would make Kubernetes
// restart a perfectly healthy process in response to someone else's
// outage, making that outage worse instead of containing it. That
// distinction is the entire reason healthz and readyz are two endpoints
// instead of one.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, statusResponse{Status: "ok"})
}

// Checker is a named readiness check. See Server.RegisterReadinessCheck.
type Checker func() error

// handleReadyz runs every registered Checker and reports 200 only if all
// pass, 503 with per-check detail otherwise. With zero checks registered
// (this milestone's state), it reports ready as soon as the process has
// finished starting — which is still meaningfully different from healthz:
// a load balancer should stop sending traffic to a pod that's alive but
// not yet ready, even though restarting that pod would be wrong.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	checks := make(map[string]string, len(s.readinessCheckers))
	ready := true
	for name, check := range s.readinessCheckers {
		if err := check(); err != nil {
			checks[name] = err.Error()
			ready = false
		} else {
			checks[name] = "ok"
		}
	}

	status := http.StatusOK
	resp := statusResponse{Status: "ready", Checks: checks}
	if !ready {
		status = http.StatusServiceUnavailable
		resp.Status = "not ready"
	}
	writeJSON(w, status, resp)
}

type pulseRequest struct {
	Message string `json:"message"`
}

// handleListPulses returns every currently stored pulse, most recent first.
func (s *Server) handleListPulses(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.List())
}

// handleCreatePulse validates and stores one pulse — pulse-api's one real
// JSON resource, backed by the bounded in-memory store rather than any
// external datastore (out of scope by design; see the architecture's
// Risks and Tradeoffs section).
func (s *Server) handleCreatePulse(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)

	var req pulseRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // catches client typos (e.g. "mesage") instead of silently ignoring them
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, `request body must be a JSON object with a "message" field`)
		return
	}
	// Decode only consumes the first JSON value in the stream and silently
	// ignores anything after it by default; this rejects a second value
	// (or trailing garbage) instead of pretending the request was clean.
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		writeError(w, http.StatusBadRequest, "request body must contain exactly one JSON object")
		return
	}

	p, err := s.store.Add(req.Message)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.logger.Info("pulse created", slog.Int64("id", p.ID))
	writeJSON(w, http.StatusCreated, p)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
