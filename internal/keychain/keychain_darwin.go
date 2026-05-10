//go:build darwin

package keychain

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// platformToken reads from the macOS Keychain via the `security` CLI.
func platformToken(service, account string) (string, error) {
	cmd := exec.Command("security", "find-generic-password", "-s", service, "-a", account, "-w")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// `security` exits 44 when the item is missing.
			if ee.ExitCode() == 44 {
				return "", ErrNotFound
			}
			return "", fmt.Errorf("keychain: security exit %d: %s",
				ee.ExitCode(), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("keychain: %w", err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}
