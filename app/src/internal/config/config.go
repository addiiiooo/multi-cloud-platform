// Package config loads pulse-api's runtime configuration from environment
// variables. Every setting has a safe default, and every setting can be
// overridden without a code change wherever the service runs next — Docker
// Compose today, Kubernetes env vars once a later milestone adds a
// Deployment.
//
// Two classes of problems are handled differently on purpose. A
// completely broken PORT is fatal: Load returns an error and the service
// should not start, because binding to whatever net.Listen does with a
// garbage address is a worse failure mode than refusing to start. An
// out-of-range or malformed MAX_PULSES, by contrast, falls back to its
// default with a logged warning: it's a resource-sizing knob, not
// something that should take the whole service down over an operator typo.
package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"time"
)

// maxPulsesCeiling bounds MAX_PULSES so a single misconfigured (or
// malicious, if this ever became attacker-controlled) environment
// variable can't make the service allocate an unbounded amount of memory
// at startup: Store pre-allocates a slice of exactly this many Pulse
// structs up front.
const maxPulsesCeiling = 10_000

// Config holds all runtime-tunable settings for the server.
type Config struct {
	// Port is the TCP port the HTTP server listens on.
	Port string

	// LogLevel controls the minimum severity written to stdout: debug, info, warn, or error.
	LogLevel string

	// ReadTimeout bounds how long the server waits to read an entire request.
	ReadTimeout time.Duration
	// WriteTimeout bounds how long a handler has to write a response.
	WriteTimeout time.Duration
	// IdleTimeout bounds how long a keep-alive connection may sit idle.
	IdleTimeout time.Duration
	// ShutdownTimeout bounds how long graceful shutdown waits for in-flight
	// requests to finish before the process exits anyway. This is what
	// makes the architecture's zero-downtime rolling-update requirement
	// possible: Kubernetes sends SIGTERM and the pod must finish in-flight
	// work before the container is killed, not before.
	ShutdownTimeout time.Duration

	// MaxPulses bounds the in-memory pulse store so a long-running process
	// cannot grow its memory footprint without limit.
	MaxPulses int
}

// Load reads Config from environment variables. It returns an error only
// for problems severe enough that starting the service anyway would be
// worse than refusing to start (currently: an unusable PORT). Anything
// else invalid falls back to its default and is logged as a warning via
// the standard library logger, since pulse-api's structured slog logger
// is itself configured from this Config and can't exist yet when Load runs.
func Load() (Config, error) {
	cfg := Config{
		Port:            getString("PORT", "8080"),
		LogLevel:        getString("LOG_LEVEL", "info"),
		ReadTimeout:     getDuration("READ_TIMEOUT", 5*time.Second),
		WriteTimeout:    getDuration("WRITE_TIMEOUT", 10*time.Second),
		IdleTimeout:     getDuration("IDLE_TIMEOUT", 120*time.Second),
		ShutdownTimeout: getDuration("SHUTDOWN_TIMEOUT", 15*time.Second),
		MaxPulses:       getInt("MAX_PULSES", 100, 1, maxPulsesCeiling),
	}

	if err := validatePort(cfg.Port); err != nil {
		return Config{}, fmt.Errorf("invalid PORT: %w", err)
	}

	return cfg, nil
}

// Addr returns the address the HTTP server should bind to.
func (c Config) Addr() string {
	return fmt.Sprintf(":%s", c.Port)
}

func validatePort(port string) error {
	n, err := strconv.Atoi(port)
	if err != nil {
		return fmt.Errorf("%q is not a valid integer: %w", port, err)
	}
	if n < 1 || n > 65535 {
		return fmt.Errorf("%d is outside the valid TCP port range 1-65535", n)
	}
	return nil
}

func getString(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Printf("config: %s=%q is not a valid duration, using default %s", key, v, def)
		return def
	}
	return d
}

// getInt reads an integer environment variable, clamping to [min, max]. An
// unset variable silently uses def (the normal case). A set-but-invalid or
// out-of-range variable also falls back to def, but logs a warning: an
// operator who thinks they changed a setting should be able to see, in the
// startup logs, that their value was rejected instead of silently applied.
func getInt(key string, def, min, max int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("config: %s=%q is not a valid integer, using default %d", key, v, def)
		return def
	}
	if n < min || n > max {
		log.Printf("config: %s=%d is outside the allowed range [%d,%d], using default %d", key, n, min, max, def)
		return def
	}
	return n
}
