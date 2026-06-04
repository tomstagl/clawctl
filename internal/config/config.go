// Package config loads clawctl runtime configuration from environment
// variables. It deliberately mirrors the variable set, defaults, and semantics
// of the bash clawctl script so the typed binary is a drop-in replacement.
package config

import (
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/tomstagl/clawctl/internal/transport/api"
)

// Config holds the resolved runtime configuration. Empty strings indicate
// "unset"; subcommands that require a value enforce it themselves with the
// documented exit-2 contract.
type Config struct {
	Host             string
	SSHHost          string
	KeychainService  string
	CacheDir         string
	Timeout          time.Duration
	ModelsTTL        time.Duration
	NoRedact         bool
	JaegerUI         string
	Log              string
	JSONOutput       bool
	RemotePath       string
	MaxResponseBytes int64
}

// Defaults returns a Config populated entirely from defaults. Useful for tests
// that want to override one field without copy-pasting the whole table.
func Defaults() Config {
	home, _ := os.UserHomeDir()
	return Config{
		KeychainService:  "openclaw-gateway-token",
		CacheDir:         filepath.Join(home, ".cache", "clawctl"),
		Timeout:          60 * time.Second,
		ModelsTTL:        60 * time.Second,
		MaxResponseBytes: api.DefaultMaxResponseBytes,
	}
}

// Load reads CLAWCTL_* environment variables and returns a populated Config.
// Numeric fields fall back to defaults if parsing fails — matches the bash
// shell's tolerant behavior with `${VAR:-default}` / arithmetic.
func Load() Config {
	c := Defaults()

	if v := os.Getenv("CLAWCTL_HOST"); v != "" {
		c.Host = v
	}
	if v := os.Getenv("CLAWCTL_SSH_HOST"); v != "" {
		c.SSHHost = v
	}
	if v := os.Getenv("CLAWCTL_KEYCHAIN_SERVICE"); v != "" {
		c.KeychainService = v
	}
	if v := os.Getenv("CLAWCTL_CACHE_DIR"); v != "" {
		c.CacheDir = v
	}
	if v := os.Getenv("CLAWCTL_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.Timeout = time.Duration(n) * time.Second
		}
	}
	if v := os.Getenv("CLAWCTL_MODELS_TTL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.ModelsTTL = time.Duration(n) * time.Second
		}
	}
	if v := os.Getenv("CLAWCTL_NO_REDACT"); v == "1" {
		c.NoRedact = true
	}
	if v := os.Getenv("CLAWCTL_JAEGER_UI"); v != "" {
		c.JaegerUI = v
	}
	if v := os.Getenv("CLAWCTL_LOG"); v != "" {
		c.Log = v
	}
	if os.Getenv("CLAWCTL_OUTPUT") == "json" {
		c.JSONOutput = true
	}
	if v := os.Getenv("CLAWCTL_REMOTE_PATH"); v != "" {
		c.RemotePath = v
	}
	if v := os.Getenv("CLAWCTL_MAX_RESPONSE_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			c.MaxResponseBytes = n
		}
	}

	return c
}
