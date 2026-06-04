package config

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/tomstagl/clawctl/internal/transport/api"
)

func TestDefaults(t *testing.T) {
	d := Defaults()
	if d.KeychainService != "openclaw-gateway-token" {
		t.Errorf("KeychainService default = %q, want %q", d.KeychainService, "openclaw-gateway-token")
	}
	if d.Timeout != 60*time.Second {
		t.Errorf("Timeout default = %v, want 60s", d.Timeout)
	}
	if d.ModelsTTL != 60*time.Second {
		t.Errorf("ModelsTTL default = %v, want 60s", d.ModelsTTL)
	}
	if d.NoRedact {
		t.Errorf("NoRedact default = true, want false")
	}
	if !filepath.IsAbs(d.CacheDir) {
		t.Errorf("CacheDir default = %q, want absolute path", d.CacheDir)
	}
	if d.MaxResponseBytes != api.DefaultMaxResponseBytes {
		t.Errorf("MaxResponseBytes default = %d, want %d", d.MaxResponseBytes, api.DefaultMaxResponseBytes)
	}
}

func TestLoadMaxResponseBytes(t *testing.T) {
	t.Run("valid override", func(t *testing.T) {
		t.Setenv("CLAWCTL_MAX_RESPONSE_BYTES", "2048")
		c := Load()
		if c.MaxResponseBytes != 2048 {
			t.Errorf("MaxResponseBytes = %d, want 2048", c.MaxResponseBytes)
		}
	})
	t.Run("invalid falls back to default", func(t *testing.T) {
		t.Setenv("CLAWCTL_MAX_RESPONSE_BYTES", "not-a-number")
		c := Load()
		if c.MaxResponseBytes != api.DefaultMaxResponseBytes {
			t.Errorf("MaxResponseBytes = %d, want default %d", c.MaxResponseBytes, api.DefaultMaxResponseBytes)
		}
	})
	t.Run("non-positive falls back to default", func(t *testing.T) {
		t.Setenv("CLAWCTL_MAX_RESPONSE_BYTES", "0")
		c := Load()
		if c.MaxResponseBytes != api.DefaultMaxResponseBytes {
			t.Errorf("MaxResponseBytes = %d, want default %d", c.MaxResponseBytes, api.DefaultMaxResponseBytes)
		}
	})
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("CLAWCTL_HOST", "http://gw.example:18789")
	t.Setenv("CLAWCTL_SSH_HOST", "user@host")
	t.Setenv("CLAWCTL_TIMEOUT", "30")
	t.Setenv("CLAWCTL_MODELS_TTL", "120")
	t.Setenv("CLAWCTL_NO_REDACT", "1")
	t.Setenv("CLAWCTL_KEYCHAIN_SERVICE", "alt-service")
	t.Setenv("CLAWCTL_CACHE_DIR", "/tmp/clawctl-test-cache")
	t.Setenv("CLAWCTL_JAEGER_UI", "http://jaeger:16686")
	t.Setenv("CLAWCTL_LOG", "json")

	c := Load()

	if c.Host != "http://gw.example:18789" {
		t.Errorf("Host = %q", c.Host)
	}
	if c.SSHHost != "user@host" {
		t.Errorf("SSHHost = %q", c.SSHHost)
	}
	if c.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v", c.Timeout)
	}
	if c.ModelsTTL != 120*time.Second {
		t.Errorf("ModelsTTL = %v", c.ModelsTTL)
	}
	if !c.NoRedact {
		t.Errorf("NoRedact = false, want true")
	}
	if c.KeychainService != "alt-service" {
		t.Errorf("KeychainService = %q", c.KeychainService)
	}
	if c.CacheDir != "/tmp/clawctl-test-cache" {
		t.Errorf("CacheDir = %q", c.CacheDir)
	}
	if c.JaegerUI != "http://jaeger:16686" {
		t.Errorf("JaegerUI = %q", c.JaegerUI)
	}
	if c.Log != "json" {
		t.Errorf("Log = %q", c.Log)
	}
}

func TestLoadInvalidNumericFallsBackToDefault(t *testing.T) {
	t.Setenv("CLAWCTL_TIMEOUT", "not-a-number")
	t.Setenv("CLAWCTL_MODELS_TTL", "")

	c := Load()
	if c.Timeout != 60*time.Second {
		t.Errorf("Timeout fallback = %v, want 60s", c.Timeout)
	}
	if c.ModelsTTL != 60*time.Second {
		t.Errorf("ModelsTTL fallback = %v, want 60s", c.ModelsTTL)
	}
}

func TestLoadNoRedactZeroIsFalse(t *testing.T) {
	t.Setenv("CLAWCTL_NO_REDACT", "0")
	c := Load()
	if c.NoRedact {
		t.Errorf("CLAWCTL_NO_REDACT=0 should not set NoRedact")
	}
}
