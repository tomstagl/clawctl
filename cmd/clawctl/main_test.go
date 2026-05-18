package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tomstagl/clawctl/internal/config"
)

func TestPrintHelpStructure(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf, config.Config{Host: "http://example:18789"})
	out := buf.String()

	for _, want := range []string{
		"clawctl health",
		"clawctl models",
		"clawctl msg",
		"clawctl stream",
		"clawctl raw",
		"clawctl cli",
		"clawctl verify",
		"clawctl trace",
		"clawctl mcp",
		"CLAWCTL_HOST",
		"CLAWCTL_SSH_HOST",
		"CLAWCTL_JAEGER_UI",
		"CLAWCTL_KEYCHAIN_SERVICE",
		"CLAWCTL_TIMEOUT",
		"CLAWCTL_NO_REDACT",
		"CLAWCTL_MODELS_TTL",
		"CLAWCTL_LOG",
		"Exit codes",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help text missing %q", want)
		}
	}

	if !strings.Contains(out, "http://example:18789") {
		t.Errorf("help text did not interpolate Host")
	}
}

func TestPrintHelpHostUnset(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf, config.Config{})
	if !strings.Contains(buf.String(), "<unset>") {
		t.Errorf("help text missing <unset> marker when Host empty")
	}
}

// TestPrintVersionEmbedsLDFlags pins the format `clawctl <version> (<commit>)`
// because the install script (US-030) and release workflow (US-029) both
// pattern-match on it: the install script uses it to refuse to overwrite a
// non-clawctl binary, and the workflow's ldflags stamp the values it consumes.
func TestPrintVersionEmbedsLDFlags(t *testing.T) {
	prevVersion, prevCommit := version, commit
	t.Cleanup(func() { version, commit = prevVersion, prevCommit })

	version = "v1.2.3"
	commit = "abcdef0"

	var buf bytes.Buffer
	printVersion(&buf)
	got := buf.String()

	want := "clawctl v1.2.3 (abcdef0)\n"
	if got != want {
		t.Errorf("printVersion = %q, want %q", got, want)
	}
}
