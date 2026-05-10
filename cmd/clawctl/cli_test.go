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
// dir to PATH for the duration of the test. The shim records argv, one
// entry per line, into <tmp>/argv.log so tests can diff what reached the
// SSH layer against what runCLI was asked to send.
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
: > "$log"
for a in "$@"; do
  printf 'ARG=<<%s>>\n' "$a" >> "$log"
done
exit 0
`
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))
	return tmp
}

// readArgv reads the recorded argv lines back out as a slice. Returns nil
// if the log doesn't exist (i.e. ssh wasn't invoked).
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
		"/usr/local/bin/oc-remote",
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
	// Locate the oc-remote anchor; everything after it is user argv.
	anchor := -1
	for i, a := range got {
		if a == "/usr/local/bin/oc-remote" {
			anchor = i
			break
		}
	}
	if anchor < 0 {
		t.Fatalf("oc-remote not in argv: %v", got)
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
	tmp := t.TempDir()
	shim := filepath.Join(tmp, "ssh")
	// Always exit 7 — proves runCLI propagates the child's status verbatim.
	body := "#!/usr/bin/env bash\nexit 7\n"
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

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
	// Last token must be /usr/local/bin/oc-remote with nothing after it.
	if len(got) == 0 || got[len(got)-1] != "/usr/local/bin/oc-remote" {
		t.Errorf("argv = %v, want trailing /usr/local/bin/oc-remote", got)
	}
}
