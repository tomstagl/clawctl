//go:build linux

package keychain

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// backendStatus distinguishes the three outcomes of probing a Linux secret
// backend so the caller can tell "not installed" from "installed but failed".
type backendStatus int

const (
	backendOK     backendStatus = iota // ran and produced a non-empty token
	backendAbsent                      // binary not on PATH
	backendFailed                      // ran but errored, or produced empty output
)

// platformToken tries, in order: secret-tool, then pass. If both miss, the
// returned error names the three configurable options (including
// CLAWCTL_TOKEN_CMD, checked before this function). Crucially, when a backend
// is installed but fails (locked keyring, wrong service name, empty entry) its
// stderr is surfaced, so the user sees the real cause instead of a generic hint
// that makes an auth failure look like a missing tool.
func platformToken(service, account string) (string, error) {
	var failures []string

	if tok, status, err := tryBackend("secret-tool", "lookup", "service", service, "account", account); status == backendOK {
		return tok, nil
	} else if status == backendFailed {
		failures = append(failures, err.Error())
	}

	if tok, status, err := tryBackend("pass", "show", "openclaw/gateway-token"); status == backendOK {
		return tok, nil
	} else if status == backendFailed {
		failures = append(failures, err.Error())
	}

	hint := fmt.Sprintf(
		"keychain: no token found on Linux; configure one of:\n"+
			"  export CLAWCTL_TOKEN_CMD='<command that prints token to stdout>'\n"+
			"  secret-tool store --label 'openclaw gateway token' service %s account %s\n"+
			"  pass insert openclaw/gateway-token",
		service, account,
	)
	if len(failures) > 0 {
		// A backend was present and failed — lead with that so the user fixes
		// the real problem rather than re-installing a tool they already have.
		return "", fmt.Errorf("%s\nbackend errors:\n  %s", hint, strings.Join(failures, "\n  "))
	}
	return "", errors.New(hint)
}

// tryBackend probes one secret backend. It returns backendAbsent (without an
// error) when the binary is not on PATH, so an uninstalled tool is silently
// skipped, and backendFailed (with a descriptive error carrying the command's
// stderr) when it ran but did not yield a token.
func tryBackend(name string, args ...string) (string, backendStatus, error) {
	if _, err := exec.LookPath(name); err != nil {
		return "", backendAbsent, nil
	}
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			detail := strings.TrimSpace(string(ee.Stderr))
			if detail == "" {
				detail = fmt.Sprintf("exit %d", ee.ExitCode())
			}
			return "", backendFailed, fmt.Errorf("%s: %s", name, detail)
		}
		return "", backendFailed, fmt.Errorf("%s: %w", name, err)
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", backendFailed, fmt.Errorf("%s: produced empty output", name)
	}
	return tok, backendOK, nil
}
