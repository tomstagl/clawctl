//go:build linux

package keychain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPlatformTokenCmdLinux verifies the CLAWCTL_TOKEN_CMD path works on Linux.
func TestPlatformTokenCmdLinux(t *testing.T) {
	t.Setenv("CLAWCTL_TOKEN_CMD", "echo linuxtoken")
	tok, err := Token("openclaw-gateway-token", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "linuxtoken" {
		t.Fatalf("got %q, want %q", tok, "linuxtoken")
	}
}

// TestPlatformTokenAllBackendsFail verifies that when secret-tool, pass, and
// CLAWCTL_TOKEN_CMD are all absent, the error message names all three options.
func TestPlatformTokenAllBackendsFail(t *testing.T) {
	// Point PATH to an empty temp dir so secret-tool and pass can't be found.
	empty := t.TempDir()
	t.Setenv("PATH", empty)
	t.Setenv("CLAWCTL_TOKEN_CMD", "")

	_, err := platformToken("openclaw-gateway-token", "testuser")
	if err == nil {
		t.Fatal("expected error when all backends fail")
	}
	msg := err.Error()
	for _, want := range []string{"CLAWCTL_TOKEN_CMD", "secret-tool", "pass"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q; got: %s", want, msg)
		}
	}
}

// TestPlatformTokenSecretTool verifies that a working secret-tool is used.
func TestPlatformTokenSecretTool(t *testing.T) {
	bin := t.TempDir()
	script := filepath.Join(bin, "secret-tool")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho secrettoken\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("CLAWCTL_TOKEN_CMD", "")

	tok, err := platformToken("openclaw-gateway-token", "testuser")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "secrettoken" {
		t.Fatalf("got %q, want %q", tok, "secrettoken")
	}
}

// TestPlatformTokenPassFallback verifies that pass is tried when secret-tool
// is absent.
func TestPlatformTokenPassFallback(t *testing.T) {
	bin := t.TempDir()
	script := filepath.Join(bin, "pass")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho passtoken\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("CLAWCTL_TOKEN_CMD", "")

	tok, err := platformToken("openclaw-gateway-token", "testuser")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "passtoken" {
		t.Fatalf("got %q, want %q", tok, "passtoken")
	}
}
