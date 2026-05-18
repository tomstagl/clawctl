package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/tomstagl/clawctl/internal/config"
	"github.com/tomstagl/clawctl/internal/logging"
)

// ocRemotePath is the absolute path to clawctl-remote on the gateway host.
// The argv-as-slice contract (US-005, mirrored here in US-020) requires this
// binary be invoked directly via ssh's argv slot — not interpolated into a
// remote shell string — so spaces, quotes, $, and backticks in argv reach
// openclaw without being re-parsed by sh.
const ocRemotePath = "/usr/local/bin/clawctl-remote"

// probeOcRemote confirms clawctl-remote is installed at the expected path on
// the host. BatchMode=yes + ConnectTimeout=5 mirror the bash wrapper so an
// unreachable or unconfigured host fails fast instead of hanging on a TTY
// password prompt. The probe is sent as one argument because ssh joins
// trailing argv into a single remote shell command anyway, and that matches
// `ssh host 'test -x ...'` in the bash version verbatim.
func probeOcRemote(ctx context.Context, sshHost string) error {
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=5",
		sshHost,
		"test -x "+ocRemotePath,
	)
	return cmd.Run()
}

// ocRemoteMissingMessage is the remediation block printed to stderr when the
// probe fails. Kept structurally identical to the bash wrapper's heredoc so
// users see the same install snippet regardless of which surface they hit.
func ocRemoteMissingMessage(sshHost string) string {
	return fmt.Sprintf(`clawctl cli: clawctl-remote not found at %s on %s.

clawctl-remote is required so argv reaches openclaw without shell-string
interpolation. Install it on the host (see the "clawctl-remote (required for
clawctl cli)" section in README.md for the full procedure):

  ssh %s 'sudo install -m 0755 /dev/stdin %s' <<'CLAWCTLREMOTE'
  #!/usr/bin/env bash
  set -euo pipefail
  export PATH="$HOME/.npm-global/bin:$PATH"
  exec openclaw "$@"
  CLAWCTLREMOTE
`, ocRemotePath, sshHost, sshHost, ocRemotePath)
}

// runCLI implements `clawctl cli ARGS...`. Probes for clawctl-remote first;
// on miss, returns 2 with a remediation message (US-021). On hit, shells out
// to ssh with ControlMaster=auto and a 10-minute persistence window so
// subsequent SSH-using subcommands reuse the connection. argv is passed as
// exec.Command varargs — never concatenated into a shell string — so the
// host shell never sees argv as text.
func runCLI(ctx context.Context, cfg config.Config, args []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	log := logging.New(cfg.Log, stderr, "cli", logging.TransportSSH)
	defer func() { code = log.Finish(code) }()
	stderr = log.Stderr()

	if cfg.SSHHost == "" {
		fmt.Fprintln(stderr, "clawctl: CLAWCTL_SSH_HOST not set. Export it (e.g. export CLAWCTL_SSH_HOST=user@host).")
		return 2
	}

	if err := probeOcRemote(ctx, cfg.SSHHost); err != nil {
		fmt.Fprint(stderr, ocRemoteMissingMessage(cfg.SSHHost))
		return 2
	}

	sshArgs := []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=~/.ssh/cm-%r@%h:%p",
		"-o", "ControlPersist=10m",
		cfg.SSHHost,
		"--",
		ocRemotePath,
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
