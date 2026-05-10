package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/tomstagl/clawctl/internal/config"
)

// ocRemotePath is the absolute path to oc-remote on the gateway host. The
// argv-as-slice contract (US-005, mirrored here in US-020) requires this
// binary be invoked directly via ssh's argv slot — not interpolated into a
// remote shell string — so spaces, quotes, $, and backticks in argv reach
// openclaw without being re-parsed by sh.
const ocRemotePath = "/usr/local/bin/oc-remote"

// runCLI implements `clawctl cli ARGS...`. Shells out to ssh with
// ControlMaster=auto and a 10-minute persistence window so subsequent SSH-
// using subcommands reuse the connection. argv is passed as exec.Command
// varargs — never concatenated into a shell string — so the host shell
// never sees argv as text.
//
// US-020 lands the wire shape only; the oc-remote presence probe (and the
// exit-2 remediation message) is US-021. Until then, a missing oc-remote
// surfaces as ssh's exit status (typically 127), passed through unchanged.
func runCLI(ctx context.Context, cfg config.Config, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if cfg.SSHHost == "" {
		fmt.Fprintln(stderr, "clawctl: CLAWCTL_SSH_HOST not set. Export it (e.g. export CLAWCTL_SSH_HOST=user@host).")
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
