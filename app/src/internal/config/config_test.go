package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != "8080" {
		t.Errorf("expected default port 8080, got %q", cfg.Port)
	}
	if cfg.MaxPulses != 100 {
		t.Errorf("expected default MaxPulses 100, got %d", cfg.MaxPulses)
	}
	if cfg.ShutdownTimeout != 15*time.Second {
		t.Errorf("expected default ShutdownTimeout 15s, got %v", cfg.ShutdownTimeout)
	}
}

func TestLoadAppliesOverrides(t *testing.T) {
	t.Setenv("PORT", "9090")
	t.Setenv("MAX_PULSES", "50")
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != "9090" {
		t.Errorf("expected overridden port 9090, got %q", cfg.Port)
	}
	if cfg.MaxPulses != 50 {
		t.Errorf("expected overridden MaxPulses 50, got %d", cfg.MaxPulses)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("expected overridden LogLevel debug, got %q", cfg.LogLevel)
	}
}

func TestLoadRejectsNonNumericPort(t *testing.T) {
	t.Setenv("PORT", "not-a-port")
	if _, err := Load(); err == nil {
		t.Fatal("expected an error for a non-numeric PORT, got nil")
	}
}

func TestLoadRejectsOutOfRangePort(t *testing.T) {
	t.Setenv("PORT", "99999")
	if _, err := Load(); err == nil {
		t.Fatal("expected an error for an out-of-range PORT, got nil")
	}
}

func TestLoadFallsBackOnInvalidMaxPulses(t *testing.T) {
	t.Setenv("MAX_PULSES", "not-a-number")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("an invalid MAX_PULSES should fall back to the default, not fail: %v", err)
	}
	if cfg.MaxPulses != 100 {
		t.Errorf("expected fallback to default 100, got %d", cfg.MaxPulses)
	}
}

func TestLoadFallsBackOnOutOfRangeMaxPulses(t *testing.T) {
	t.Setenv("MAX_PULSES", "999999999")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("an out-of-range MAX_PULSES should fall back to the default, not fail: %v", err)
	}
	if cfg.MaxPulses != 100 {
		t.Errorf("expected fallback to default 100, got %d", cfg.MaxPulses)
	}
}
