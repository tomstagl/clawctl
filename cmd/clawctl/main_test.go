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
