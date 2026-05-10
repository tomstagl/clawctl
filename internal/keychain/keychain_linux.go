//go:build linux

package keychain

import (
	"fmt"
	"os/exec"
	"strings"
)

// platformToken tries, in order: secret-tool, then pass. If both fail the
// returned error names all three available options (including CLAWCTL_TOKEN_CMD
// which is checked before this function is called).
func platformToken(service, account string) (string, error) {
	if tok, err := runCmd("secret-tool", "lookup", "service", service, "account", account); err == nil && tok != "" {
		return tok, nil
	}

	if tok, err := runCmd("pass", "show", "openclaw/gateway-token"); err == nil && tok != "" {
		return tok, nil
	}

	return "", fmt.Errorf(
		"keychain: no token found on Linux; configure one of:\n"+
			"  export CLAWCTL_TOKEN_CMD='<command that prints token to stdout>'\n"+
			"  secret-tool store --label 'openclaw gateway token' service %s account %s\n"+
			"  pass insert openclaw/gateway-token",
		service, account,
	)
}

func runCmd(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
