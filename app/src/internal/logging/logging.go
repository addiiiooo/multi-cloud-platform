// Package logging configures pulse-api's structured logger.
//
// Every log line is written as a single JSON object to stdout, per the
// architecture's observability design: a node-level agent (CloudWatch
// Container Insights on EKS, the Cloud Logging agent on GKE) tails
// container stdout with zero awareness of which cloud it's running in.
// Only the transport (stdout, JSON) is fixed here; where those lines end up
// is entirely an infrastructure concern wired up in a later milestone.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// New builds a JSON-handler slog.Logger writing to stdout at the given
// level. An unrecognized level falls back to info rather than failing to
// start the process over a typo in an environment variable.
func New(level string) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(level),
	})
	return slog.New(handler)
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
