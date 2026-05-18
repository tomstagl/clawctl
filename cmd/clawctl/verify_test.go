package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tomstagl/clawctl/internal/config"
)

// installFakeGitGH writes recording shims for `git` and `gh` to a tempdir
// and prepends that dir to PATH for the test. The shims read their behaviour
// from env vars set by individual cases. Mirrors the cli_test.go approach so
// we keep the "no new test deps" rule from CLAUDE.md.
//
// Env knobs:
//
//	GIT_CATFILE_T_OUT      stdout for `git cat-file -t <hash>` (e.g. "commit\n")
//	GIT_CATFILE_T_EXIT     exit code for the same (default 0)
//	GIT_CATFILE_E_EXIT     exit code for `git cat-file -e <ref>:<path>` (default 0)
//	GIT_REVPARSE_OUT       stdout for `git rev-parse --show-toplevel`
//	GIT_REVPARSE_EXIT      exit code for the same (default 0)
//	GH_OUT                 stdout for `gh pr|issue view ...`
//	GH_EXIT                exit code for the same (default 0)
func installFakeGitGH(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-git/gh PATH shim assumes a POSIX shell; clawctl is macOS-only by design")
	}
	tmp := t.TempDir()

	gitShim := `#!/usr/bin/env bash
case "$1" in
  cat-file)
    case "$2" in
      -t)
        printf '%s' "${GIT_CATFILE_T_OUT:-commit\n}"
        exit "${GIT_CATFILE_T_EXIT:-0}" ;;
      -e)
        exit "${GIT_CATFILE_E_EXIT:-0}" ;;
    esac ;;
  rev-parse)
    printf '%s' "${GIT_REVPARSE_OUT:-/repo}"
    exit "${GIT_REVPARSE_EXIT:-0}" ;;
esac
exit 1
`
	if err := os.WriteFile(filepath.Join(tmp, "git"), []byte(gitShim), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}

	ghShim := `#!/usr/bin/env bash
printf '%s' "${GH_OUT:-}"
exit "${GH_EXIT:-0}"
`
	if err := os.WriteFile(filepath.Join(tmp, "gh"), []byte(ghShim), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}

	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))
	return tmp
}

func TestVerify_Help(t *testing.T) {
	for _, sub := range []string{"", "help", "-h", "--help"} {
		var stdout, stderr bytes.Buffer
		args := []string{}
		if sub != "" {
			args = []string{sub}
		}
		code := runVerify(context.Background(), config.Config{}, args, &stdout, &stderr)
		if code != 2 {
			t.Errorf("sub=%q: exit = %d, want 2", sub, code)
		}
		if !strings.Contains(stderr.String(), "clawctl verify <kind>") {
			t.Errorf("sub=%q: stderr missing help banner: %q", sub, stderr.String())
		}
	}
}

func TestVerify_UnknownKind(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runVerify(context.Background(), config.Config{}, []string{"frobnicate"}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown kind 'frobnicate'") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestVerify_Commit_Verified(t *testing.T) {
	installFakeGitGH(t)
	t.Setenv("GIT_CATFILE_T_OUT", "commit\n")
	t.Setenv("GIT_CATFILE_T_EXIT", "0")

	var stdout, stderr bytes.Buffer
	code := runVerify(context.Background(), config.Config{}, []string{"commit", "deadbeef"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	if got := stdout.String(); got != "verified: commit deadbeef\n" {
		t.Errorf("stdout = %q", got)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestVerify_Commit_NotFound(t *testing.T) {
	installFakeGitGH(t)
	t.Setenv("GIT_CATFILE_T_EXIT", "128")
	t.Setenv("GIT_REVPARSE_OUT", "/some/repo\n")

	var stdout, stderr bytes.Buffer
	code := runVerify(context.Background(), config.Config{}, []string{"commit", "nope"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	want := "unverified: commit nope not found in /some/repo\n"
	if got := stderr.String(); got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
}

func TestVerify_Commit_WrongType(t *testing.T) {
	// `git cat-file -t` returning "tree" must read as unverified —
	// the bash branch only accepts an exact "commit" string.
	installFakeGitGH(t)
	t.Setenv("GIT_CATFILE_T_OUT", "tree\n")
	t.Setenv("GIT_CATFILE_T_EXIT", "0")
	t.Setenv("GIT_REVPARSE_OUT", "/repo\n")

	var stdout, stderr bytes.Buffer
	code := runVerify(context.Background(), config.Config{}, []string{"commit", "abc123"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "unverified: commit abc123 not found") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestVerify_Commit_MissingArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runVerify(context.Background(), config.Config{}, []string{"commit"}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if got := stderr.String(); got != "usage: clawctl verify commit <hash>\n" {
		t.Errorf("stderr = %q", got)
	}
}

func TestVerify_PR_Verified(t *testing.T) {
	installFakeGitGH(t)
	t.Setenv("GH_OUT", `{"state":"OPEN","url":"https://github.com/o/r/pull/12","title":"Add a thing"}`)
	t.Setenv("GH_EXIT", "0")

	var stdout, stderr bytes.Buffer
	code := runVerify(context.Background(), config.Config{}, []string{"pr", "o/r#12"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	want := "verified: OPEN — Add a thing — https://github.com/o/r/pull/12\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestVerify_PR_Inaccessible(t *testing.T) {
	installFakeGitGH(t)
	t.Setenv("GH_EXIT", "1")

	var stdout, stderr bytes.Buffer
	code := runVerify(context.Background(), config.Config{}, []string{"pr", "o/r#404"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if got := stderr.String(); got != "unverified: PR o/r#404 not accessible\n" {
		t.Errorf("stderr = %q", got)
	}
}

func TestVerify_PR_BadSpec(t *testing.T) {
	cases := []struct {
		spec string
		want string
	}{
		{"o/r", "usage: clawctl verify pr <repo>#<num>\n"},
		{"o/r#", "usage: clawctl verify pr <repo>#<num>\n"},
	}
	for _, c := range cases {
		var stdout, stderr bytes.Buffer
		code := runVerify(context.Background(), config.Config{}, []string{"pr", c.spec}, &stdout, &stderr)
		if code != 2 {
			t.Errorf("spec=%q: exit = %d, want 2", c.spec, code)
		}
		if got := stderr.String(); got != c.want {
			t.Errorf("spec=%q: stderr = %q, want %q", c.spec, got, c.want)
		}
	}
}

func TestVerify_PR_MissingArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runVerify(context.Background(), config.Config{}, []string{"pr"}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	want := "usage: clawctl verify pr <repo>#<num>  (repo=owner/name)\n"
	if got := stderr.String(); got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
}

func TestVerify_Issue_Verified(t *testing.T) {
	installFakeGitGH(t)
	t.Setenv("GH_OUT", `{"state":"CLOSED","url":"https://github.com/o/r/issues/3","title":"Bug"}`)

	var stdout, stderr bytes.Buffer
	code := runVerify(context.Background(), config.Config{}, []string{"issue", "o/r#3"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	want := "verified: CLOSED — Bug — https://github.com/o/r/issues/3\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestVerify_Issue_Inaccessible(t *testing.T) {
	installFakeGitGH(t)
	t.Setenv("GH_EXIT", "1")

	var stdout, stderr bytes.Buffer
	code := runVerify(context.Background(), config.Config{}, []string{"issue", "o/r#7"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if got := stderr.String(); got != "unverified: issue o/r#7 not accessible\n" {
		t.Errorf("stderr = %q", got)
	}
}

func TestVerify_Issue_MissingArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runVerify(context.Background(), config.Config{}, []string{"issue"}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if got := stderr.String(); got != "usage: clawctl verify issue <repo>#<num>\n" {
		t.Errorf("stderr = %q", got)
	}
}

func TestVerify_File_WorkingTree(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "present.txt")
	if err := os.WriteFile(path, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runVerify(context.Background(), config.Config{}, []string{"file", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	want := "verified: " + path + " (working tree)\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestVerify_File_NotInWorkingTree(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing.txt")

	var stdout, stderr bytes.Buffer
	code := runVerify(context.Background(), config.Config{}, []string{"file", path}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	want := "unverified: " + path + " not present in working tree\n"
	if got := stderr.String(); got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
}

func TestVerify_File_AtRef_Found(t *testing.T) {
	installFakeGitGH(t)
	t.Setenv("GIT_CATFILE_E_EXIT", "0")

	var stdout, stderr bytes.Buffer
	code := runVerify(context.Background(), config.Config{}, []string{"file", "README.md", "HEAD"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	if got := stdout.String(); got != "verified: README.md @ HEAD\n" {
		t.Errorf("stdout = %q", got)
	}
}

func TestVerify_File_AtRef_NotFound(t *testing.T) {
	installFakeGitGH(t)
	t.Setenv("GIT_CATFILE_E_EXIT", "1")

	var stdout, stderr bytes.Buffer
	code := runVerify(context.Background(), config.Config{}, []string{"file", "README.md", "v0.0.0"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if got := stderr.String(); got != "unverified: README.md not present at v0.0.0\n" {
		t.Errorf("stderr = %q", got)
	}
}

func TestVerify_File_MissingArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runVerify(context.Background(), config.Config{}, []string{"file"}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if got := stderr.String(); got != "usage: clawctl verify file <path> [<ref>]\n" {
		t.Errorf("stderr = %q", got)
	}
}
