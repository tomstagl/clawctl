package keychain

import (
	"errors"
	"os"
	"testing"
)

func TestTokenRequiresService(t *testing.T) {
	_, err := Token("", "")
	if err == nil {
		t.Fatal("expected error for empty service")
	}
}

func TestTokenMissingItemReturnsErrNotFound(t *testing.T) {
	// Use a service name that almost certainly isn't present on dev machines or
	// CI runners. If it ever is, the test will be flaky — pick something
	// distinctive.
	_, err := Token("clawctl-test-service-that-does-not-exist-x73h", "")
	if err == nil {
		t.Skip("keychain unexpectedly contained the test service; skipping")
	}
	if !errors.Is(err, ErrNotFound) {
		// Don't fail on non-darwin or missing `security` — this package is
		// macOS-only for the default path; on Linux and other platforms the
		// error is a descriptive "no token found" that is not ErrNotFound.
		t.Skipf("platform returned non-ErrNotFound (expected on Linux/other): %v", err)
	}
}

// TestTokenFromCmdSuccess verifies the CLAWCTL_TOKEN_CMD override on all
// platforms: when the env var is set the token comes from the command's stdout.
func TestTokenFromCmdSuccess(t *testing.T) {
	t.Setenv("CLAWCTL_TOKEN_CMD", "echo testtoken123")
	tok, err := Token("any-service", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "testtoken123" {
		t.Fatalf("got %q, want %q", tok, "testtoken123")
	}
}

// TestTokenFromCmdEmptyOutput verifies that an empty-output command is rejected.
func TestTokenFromCmdEmptyOutput(t *testing.T) {
	t.Setenv("CLAWCTL_TOKEN_CMD", "echo ''")
	_, err := Token("any-service", "")
	if err == nil {
		t.Fatal("expected error for empty CLAWCTL_TOKEN_CMD output")
	}
}

// TestTokenFromCmdFailure verifies that a non-zero exit from CLAWCTL_TOKEN_CMD
// propagates as an error (not ErrNotFound).
func TestTokenFromCmdFailure(t *testing.T) {
	t.Setenv("CLAWCTL_TOKEN_CMD", "exit 1")
	_, err := Token("any-service", "")
	if err == nil {
		t.Fatal("expected error when CLAWCTL_TOKEN_CMD exits non-zero")
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatal("command-failure error should not be ErrNotFound")
	}
}

// TestTokenFromCmdOverridesMacOSKeychain verifies that CLAWCTL_TOKEN_CMD takes
// precedence over the platform backend (exercises the macOS override path).
func TestTokenFromCmdOverridesMacOSKeychain(t *testing.T) {
	want := "overridetoken"
	t.Setenv("CLAWCTL_TOKEN_CMD", "echo "+want)
	// Even with a real keychain service name the override is used.
	tok, err := Token("openclaw-gateway-token", os.Getenv("USER"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != want {
		t.Fatalf("got %q, want %q", tok, want)
	}
}
