package main

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tomstagl/clawctl/internal/config"
)

// installFakeSSH writes a recording shim to a tempdir and prepends that
// dir to PATH for the duration of the test. The shim distinguishes the
// clawctl-remote probe (`ssh ... 'test -x /usr/local/bin/clawctl-remote'`) from the
// real invocation by inspecting the trailing argv after option flags and
// the host token. Probe behaviour is controlled by env vars set by the
// caller (OCREMOTE_PROBE_EXIT, OCREMOTE_CALL_EXIT) so individual tests can
// simulate "clawctl-remote present" vs. "clawctl-remote missing" without rewriting
// the shim. argv for the real call is recorded one entry per line in
// <tmp>/argv.log so tests can diff what reached the SSH layer against
// what runCLI was asked to send.
//
// We deliberately mirror test/cli-hardening.sh's strategy here: a fake
// `ssh` on PATH lets us assert end-to-end argv preservation without
// pulling in a Go SSH server library. The "no new runtime deps" rule
// (CLAUDE.md) extends to test dependencies in spirit — the bash side
// uses the same approach for parity reasons (test/cli-hardening.sh).
func installFakeSSH(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-ssh PATH shim assumes a POSIX shell; clawctl is macOS-only by design")
	}
	tmp := t.TempDir()
	shim := filepath.Join(tmp, "ssh")
	body := `#!/usr/bin/env bash
log="` + filepath.Join(tmp, "argv.log") + `"
saved=("$@")

# Strip option flags and capture the host token, mirroring the parser in
# test/cli-hardening.sh so probe-vs-call detection stays in lockstep.
while [ $# -gt 0 ]; do
  case "$1" in
    -o) shift 2 ;;
    -*) shift ;;
    *)  shift; break ;;
  esac
done
if [ "${1:-}" = "--" ]; then shift; fi

# Probe form: a single argument beginning with "test -x".
if [ "$#" -eq 1 ] && [[ "$1" == "test -x"* ]]; then
  exit "${OCREMOTE_PROBE_EXIT:-0}"
fi

# Real-call form: log argv then exit with OCREMOTE_CALL_EXIT.
: > "$log"
if [ ${#saved[@]} -gt 0 ]; then
  for a in "${saved[@]}"; do
    printf 'ARG=<<%s>>\n' "$a" >> "$log"
  done
fi
exit "${OCREMOTE_CALL_EXIT:-0}"
`
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))
	return tmp
}

// readArgv reads the recorded argv lines back out as a slice. Returns nil
// if the log doesn't exist (i.e. the real-call branch of the shim was
// never reached — typically because the probe failed first).
func readArgv(t *testing.T, dir string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "argv.log"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		if !strings.HasPrefix(line, "ARG=<<") || !strings.HasSuffix(line, ">>") {
			t.Fatalf("malformed argv.log line: %q", line)
		}
		out = append(out, strings.TrimSuffix(strings.TrimPrefix(line, "ARG=<<"), ">>"))
	}
	return out
}

func TestRunCLI_MissingSSHHost(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runCLI(context.Background(), config.Config{}, []string{"agents", "list"}, nil, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "CLAWCTL_SSH_HOST not set") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunCLI_PassesControlMasterArgsAndArgv(t *testing.T) {
	tmp := installFakeSSH(t)

	var stdout, stderr bytes.Buffer
	cfg := config.Config{SSHHost: "user@example.test"}
	code := runCLI(context.Background(), cfg, []string{"agents", "list"}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}

	got := readArgv(t, tmp)
	want := []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=~/.ssh/cm-%r@%h:%p",
		"-o", "ControlPersist=10m",
		"user@example.test",
		"--",
		"/usr/local/bin/clawctl-remote",
		"agents",
		"list",
	}
	if len(got) != len(want) {
		t.Fatalf("argv len = %d, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("argv[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestRunCLI_PreservesSpacesQuotesDollarsBackticks(t *testing.T) {
	tmp := installFakeSSH(t)

	tricky := []string{
		"hello world",                   // spaces
		"it's a 'mixed' \"quote\" test", // quotes (single + double)
		"$(rm -rf /); `id`; |&;<>",      // shell metacharacters: $, backticks, pipes
	}

	var stdout, stderr bytes.Buffer
	cfg := config.Config{SSHHost: "user@example.test"}
	code := runCLI(context.Background(), cfg, append([]string{"msg"}, tricky...), nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}

	got := readArgv(t, tmp)
	// Locate the clawctl-remote anchor; everything after it is user argv.
	anchor := -1
	for i, a := range got {
		if a == "/usr/local/bin/clawctl-remote" {
			anchor = i
			break
		}
	}
	if anchor < 0 {
		t.Fatalf("clawctl-remote not in argv: %v", got)
	}
	user := got[anchor+1:]
	wantUser := append([]string{"msg"}, tricky...)
	if len(user) != len(wantUser) {
		t.Fatalf("user argv len = %d, want %d\ngot:  %v\nwant: %v", len(user), len(wantUser), user, wantUser)
	}
	for i, w := range wantUser {
		if user[i] != w {
			t.Errorf("user argv[%d] = %q, want %q", i, user[i], w)
		}
	}
}

func TestRunCLI_ForwardsExitCode(t *testing.T) {
	// Fake ssh: probe succeeds, real call exits 7 — proves runCLI
	// propagates the child's status verbatim instead of remapping to 2.
	installFakeSSH(t)
	t.Setenv("OCREMOTE_CALL_EXIT", "7")

	var stdout, stderr bytes.Buffer
	cfg := config.Config{SSHHost: "user@example.test"}
	code := runCLI(context.Background(), cfg, []string{"agents", "list"}, nil, &stdout, &stderr)
	if code != 7 {
		t.Errorf("exit = %d, want 7 (passthrough)", code)
	}
}

func TestRunCLI_NoArgsStillReachesOcRemote(t *testing.T) {
	tmp := installFakeSSH(t)

	var stdout, stderr bytes.Buffer
	cfg := config.Config{SSHHost: "host.test"}
	code := runCLI(context.Background(), cfg, nil, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}

	got := readArgv(t, tmp)
	// Last token must be /usr/local/bin/clawctl-remote with nothing after it.
	if len(got) == 0 || got[len(got)-1] != "/usr/local/bin/clawctl-remote" {
		t.Errorf("argv = %v, want trailing /usr/local/bin/clawctl-remote", got)
	}
}

// TestRunCLI_ExitsWhenOcRemoteMissing covers the US-021 acceptance criterion:
// when the probe fails (clawctl-remote absent on the host) we must exit 2 with a
// remediation message naming the install path, and we must NOT proceed to
// invoke ssh a second time. Mirrors test/cli-hardening.sh test 2.
func TestRunCLI_ExitsWhenOcRemoteMissing(t *testing.T) {
	tmp := installFakeSSH(t)
	t.Setenv("OCREMOTE_PROBE_EXIT", "1")

	var stdout, stderr bytes.Buffer
	cfg := config.Config{SSHHost: "user@absent.test"}
	code := runCLI(context.Background(), cfg, []string{"agents", "list"}, nil, &stdout, &stderr)

	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}

	msg := stderr.String()
	for _, want := range []string{
		"clawctl-remote not found",
		"/usr/local/bin/clawctl-remote",
		"user@absent.test",
		"README.md",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("stderr missing %q\nfull stderr:\n%s", want, msg)
		}
	}

	// The real-call branch must not have run — argv.log should not exist.
	if got := readArgv(t, tmp); got != nil {
		t.Errorf("real ssh call happened despite probe failure: argv=%v", got)
	}
}

// TestRunCLI_ProceedsWhenOcRemotePresent covers the US-021 acceptance
// criterion that a successful probe lets the call proceed. We assert both
// the exit code (0) and that the real-call argv reached the ssh layer.
func TestRunCLI_ProceedsWhenOcRemotePresent(t *testing.T) {
	tmp := installFakeSSH(t)
	t.Setenv("OCREMOTE_PROBE_EXIT", "0")

	var stdout, stderr bytes.Buffer
	cfg := config.Config{SSHHost: "user@present.test"}
	code := runCLI(context.Background(), cfg, []string{"agents", "list"}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}

	got := readArgv(t, tmp)
	if got == nil {
		t.Fatal("real ssh call did not happen after successful probe")
	}
	// Sanity: the recorded argv must end with the expected clawctl-remote slice.
	tail := []string{"--", "/usr/local/bin/clawctl-remote", "agents", "list"}
	if len(got) < len(tail) {
		t.Fatalf("argv too short: %v", got)
	}
	for i, w := range tail {
		j := len(got) - len(tail) + i
		if got[j] != w {
			t.Errorf("argv[%d] = %q, want %q (full: %v)", j, got[j], w, got)
		}
	}
}
