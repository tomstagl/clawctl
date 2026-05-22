package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/tomstagl/clawctl/internal/config"
	"github.com/tomstagl/clawctl/internal/logging"
)

// defaultRemotePath is the default absolute path to clawctl-remote on the
// gateway host. Override with CLAWCTL_REMOTE_PATH for hosts where /usr/local/bin
// is not writable without root (e.g. ~/.local/bin/clawctl-remote).
const defaultRemotePath = "/usr/local/bin/clawctl-remote"

func resolveRemotePath(cfg config.Config) string {
	if cfg.RemotePath != "" {
		return cfg.RemotePath
	}
	return defaultRemotePath
}

// errRemoteStale is returned by probeClawctlRemote when the script is present
// but carries a different version than the running binary.
var errRemoteStale = errors.New("clawctl-remote is installed but outdated")

// clawctlRemoteScript returns the versioned shim to install on the gateway
// host. The version comment in line 2 lets probeClawctlRemote detect whether
// the installed copy is current without a separate version endpoint.
func clawctlRemoteScript() string {
	v := version
	if v == "" {
		v = "dev"
	}
	return "#!/usr/bin/env bash\n" +
		"# clawctl-remote " + v + "\n" +
		"set -euo pipefail\n" +
		"export PATH=\"$HOME/.npm-global/bin:$PATH\"\n" +
		"exec openclaw \"$@\"\n"
}

// probeClawctlRemote checks whether the correct version of clawctl-remote is
// installed on the host. It reads the first three lines of the script (one
// SSH round-trip) and checks for the version marker. Returns nil when the
// installed version matches, errRemoteStale when a different version is
// present, or a non-nil error when the script is absent entirely.
func probeClawctlRemote(ctx context.Context, sshHost, remotePath string) error {
	v := version
	if v == "" {
		v = "dev"
	}
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=5",
		sshHost,
		"test -x "+remotePath+" && head -3 "+remotePath,
	)
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	if !strings.Contains(string(out), "# clawctl-remote "+v) {
		return errRemoteStale
	}
	return nil
}

// installClawctlRemote pipes the versioned shim to the host via SSH stdin.
// mkdir -p ensures the parent directory exists for user-writable paths like
// ~/.local/bin; plain install (no sudo) works for any path the remote user owns.
func installClawctlRemote(ctx context.Context, sshHost, remotePath string, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		sshHost,
		"mkdir -p $(dirname "+remotePath+") && install -m 0755 /dev/stdin "+remotePath,
	)
	cmd.Stdin = strings.NewReader(clawctlRemoteScript())
	cmd.Stderr = stderr
	return cmd.Run()
}

// manualInstallMessage is the remediation block shown when auto-install fails.
func manualInstallMessage(sshHost, remotePath string) string {
	return fmt.Sprintf(`
To install manually:

  ssh %s 'mkdir -p $(dirname %s) && install -m 0755 /dev/stdin %s' <<'CLAWCTLREMOTE'
%sCLAWCTLREMOTE
`, sshHost, remotePath, remotePath, clawctlRemoteScript())
}

// runCLI implements `clawctl cli ARGS...`. It ensures clawctl-remote is
// present and current on the host, auto-installing or updating it as needed,
// then passes argv directly via ssh's exec slot.
func runCLI(ctx context.Context, cfg config.Config, args []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	log := logging.New(cfg.Log, stderr, "cli", logging.TransportSSH)
	defer func() { code = log.Finish(code) }()
	stderr = log.Stderr()

	if cfg.SSHHost == "" {
		fmt.Fprintln(stderr, "clawctl: CLAWCTL_SSH_HOST not set. Export it (e.g. export CLAWCTL_SSH_HOST=user@host).")
		return 2
	}

	remotePath := resolveRemotePath(cfg)
	if err := probeClawctlRemote(ctx, cfg.SSHHost, remotePath); err != nil {
		action := "installing"
		if errors.Is(err, errRemoteStale) {
			action = "updating"
		}
		fmt.Fprintf(stderr, "clawctl-remote: %s on %s...\n", action, cfg.SSHHost)
		if installErr := installClawctlRemote(ctx, cfg.SSHHost, remotePath, stderr); installErr != nil {
			fmt.Fprintf(stderr, "clawctl cli: could not install clawctl-remote on %s: %v\n", cfg.SSHHost, installErr)
			fmt.Fprint(stderr, manualInstallMessage(cfg.SSHHost, remotePath))
			return 2
		}
		fmt.Fprintf(stderr, "clawctl-remote installed at %s on %s\n", remotePath, cfg.SSHHost)
	}

	sshArgs := []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=~/.ssh/cm-%r@%h:%p",
		"-o", "ControlPersist=10m",
		cfg.SSHHost,
		"--",
		remotePath,
	}
	sshArgs = append(sshArgs, args...)

	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		fmt.Fprintf(stderr, "clawctl cli: %v\n", err)
		return 2
	}
	return 0
}

func cliCmd(cfg config.Config, args []string) {
	code := runCLI(context.Background(), cfg, args, os.Stdin, os.Stdout, os.Stderr)
	os.Exit(code)
}
