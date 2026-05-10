// Package keychain reads bearer tokens from the platform credential store.
// CLAWCTL_TOKEN_CMD is honoured on every platform as an override; falling back
// to a platform-specific backend (macOS Keychain, Linux secret-tool/pass).
//
// Design principle #2 is preserved: CLAWCTL_TOKEN env var is never consulted.
package keychain

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ErrNotFound is returned when the credential store has no matching item.
// Callers can branch on this to print a remediation hint.
var ErrNotFound = errors.New("keychain: item not found")

// Token reads the bearer token for (service, account) from the credential
// store. When account is empty the current $USER is used. CLAWCTL_TOKEN_CMD,
// if set, is always tried first on every platform.
func Token(service, account string) (string, error) {
	if service == "" {
		return "", errors.New("keychain: service name required")
	}
	if account == "" {
		account = os.Getenv("USER")
	}
	if cmd := os.Getenv("CLAWCTL_TOKEN_CMD"); cmd != "" {
		return tokenFromCmd(cmd)
	}
	return platformToken(service, account)
}

// tokenFromCmd runs an arbitrary shell command and returns trimmed stdout.
func tokenFromCmd(cmd string) (string, error) {
	out, err := exec.Command("sh", "-c", cmd).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("keychain: CLAWCTL_TOKEN_CMD exit %d: %s",
				ee.ExitCode(), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("keychain: CLAWCTL_TOKEN_CMD: %w", err)
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", errors.New("keychain: CLAWCTL_TOKEN_CMD produced empty output")
	}
	return tok, nil
}
