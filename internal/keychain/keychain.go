// Package keychain reads bearer tokens from the macOS Keychain. It shells
// out to `security find-generic-password -w` so the token never lives in
// process memory longer than necessary and so we never have to re-implement
// keychain access ourselves.
//
// This is the only token source clawctl supports; design principle #2
// forbids env-var or on-disk fallbacks. Linux/Windows are explicitly out of
// scope.
package keychain

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ErrNotFound is returned when the keychain has no matching item. Callers
// can branch on this to print a remediation message instead of the raw
// `security` exit details.
var ErrNotFound = errors.New("keychain: item not found")

// Token reads the password for the generic-password item identified by
// (service, account). When account is empty, the current user from $USER is
// used to match the bash script's `-a "$USER"` invocation.
func Token(service, account string) (string, error) {
	if service == "" {
		return "", errors.New("keychain: service name required")
	}
	if account == "" {
		account = os.Getenv("USER")
	}

	cmd := exec.Command("security", "find-generic-password", "-s", service, "-a", account, "-w")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// `security` exits 44 when the item is missing; surface a typed error
			// so callers can produce a tailored message without re-grepping stderr.
			if ee.ExitCode() == 44 {
				return "", ErrNotFound
			}
			return "", fmt.Errorf("keychain: security exit %d: %s", ee.ExitCode(), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("keychain: %w", err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}
