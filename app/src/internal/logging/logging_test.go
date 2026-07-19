package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"DEBUG":   slog.LevelDebug,
		"info":    slog.LevelInfo,
		"":        slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
		"bogus":   slog.LevelInfo, // unrecognized input must fail safe to info, not error out
	}
	for input, want := range cases {
		if got := parseLevel(input); got != want {
			t.Errorf("parseLevel(%q) = %v, want %v", input, got, want)
		}
	}
}

// TestHandlerRespectsParsedLevelAndEmitsJSON exercises the same
// construction New uses (a JSON handler at a parsed level) against a
// buffer instead of os.Stdout, so the level-filtering and output-shape
// behavior can be asserted precisely without capturing process stdout.
func TestHandlerRespectsParsedLevelAndEmitsJSON(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: parseLevel("warn")}))

	logger.Info("should be filtered out")
	if buf.Len() != 0 {
		t.Fatalf("expected an info-level log to be filtered at warn level, got: %s", buf.String())
	}

	logger.Warn("should appear", "key", "value")

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &entry); err != nil {
		t.Fatalf("expected a single valid JSON log line, got %q: %v", buf.String(), err)
	}
	if entry["msg"] != "should appear" {
		t.Errorf(`expected msg "should appear", got %v`, entry["msg"])
	}
	if entry["key"] != "value" {
		t.Errorf(`expected key "value", got %v`, entry["key"])
	}
	if _, ok := entry["level"]; !ok {
		t.Errorf("expected a level field in the JSON output, got: %v", entry)
	}
}

func TestNewReturnsAJSONHandlerLogger(t *testing.T) {
	logger := New("info")
	if logger == nil {
		t.Fatal("expected New to return a non-nil logger")
	}
	if _, ok := logger.Handler().(*slog.JSONHandler); !ok {
		t.Errorf("expected New to configure a *slog.JSONHandler, got %T", logger.Handler())
	}
}
