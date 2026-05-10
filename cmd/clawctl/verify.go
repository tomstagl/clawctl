package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/tomstagl/clawctl/internal/config"
)

// runVerify implements `clawctl verify <kind> <args>`. It mirrors the bash
// dispatcher's behaviour byte-for-byte so the parity test in
// test/parity-verify.sh diffs cleanly. The kinds split as:
//
//   commit <hash>            — `git cat-file -t` returns "commit"
//   pr     <repo>#<num>      — `gh pr view --json state,url,title` succeeds
//   issue  <repo>#<num>      — `gh issue view --json state,url,title` succeeds
//   file   <path> [<ref>]    — working-tree presence, or `git cat-file -e ref:path`
//
// Exit codes: 0 verified, 1 unverified, 2 usage/ambiguous (matches the
// `Subcommand-specific exit codes` row in `clawctl help`).
func runVerify(ctx context.Context, _ config.Config, args []string, stdout, stderr io.Writer) int {
	sub := ""
	var rest []string
	if len(args) > 0 {
		sub = args[0]
		rest = args[1:]
	}

	switch sub {
	case "commit":
		return verifyCommit(ctx, rest, stdout, stderr)
	case "pr":
		return verifyGH(ctx, "pr", rest, stdout, stderr)
	case "issue":
		return verifyGH(ctx, "issue", rest, stdout, stderr)
	case "file":
		return verifyFile(ctx, rest, stdout, stderr)
	case "", "help", "-h", "--help":
		fmt.Fprint(stderr, verifyHelpText())
		return 2
	default:
		fmt.Fprintf(stderr, "clawctl verify: unknown kind '%s' (try 'clawctl verify help')\n", sub)
		return 2
	}
}

func verifyHelpText() string {
	return `clawctl verify <kind> <args>
  commit <hash>             — git cat-file -t == commit
  pr     <owner/name>#<n>   — gh pr view returns
  issue  <owner/name>#<n>   — gh issue view returns
  file   <path> [<ref>]     — file exists in working tree (or at ref)

Exit codes: 0 verified, 1 not found, 2 usage/ambiguous.
`
}

func verifyCommit(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	hash := ""
	if len(args) > 0 {
		hash = args[0]
	}
	if hash == "" {
		fmt.Fprintln(stderr, "usage: clawctl verify commit <hash>")
		return 2
	}

	out, err := exec.CommandContext(ctx, "git", "cat-file", "-t", hash).Output()
	if err == nil && strings.TrimSpace(string(out)) == "commit" {
		fmt.Fprintf(stdout, "verified: commit %s\n", hash)
		return 0
	}

	root := gitRoot(ctx)
	fmt.Fprintf(stderr, "unverified: commit %s not found in %s\n", hash, root)
	return 1
}

// gitRoot returns the toplevel for `git rev-parse --show-toplevel`; falls
// back to the current working directory when not in a repo, mirroring the
// bash form `$(git rev-parse --show-toplevel 2>/dev/null || pwd)`.
func gitRoot(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output()
	if err == nil {
		s := strings.TrimSpace(string(out))
		if s != "" {
			return s
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return "."
}

func verifyGH(ctx context.Context, kind string, args []string, stdout, stderr io.Writer) int {
	spec := ""
	if len(args) > 0 {
		spec = args[0]
	}
	if spec == "" {
		if kind == "pr" {
			fmt.Fprintln(stderr, "usage: clawctl verify pr <repo>#<num>  (repo=owner/name)")
		} else {
			fmt.Fprintln(stderr, "usage: clawctl verify issue <repo>#<num>")
		}
		return 2
	}

	idx := strings.Index(spec, "#")
	if idx < 0 || idx == len(spec)-1 {
		fmt.Fprintf(stderr, "usage: clawctl verify %s <repo>#<num>\n", kind)
		return 2
	}
	repo := spec[:idx]
	num := spec[idx+1:]

	out, err := exec.CommandContext(ctx, "gh", kind, "view", num,
		"--repo", repo, "--json", "state,url,title").Output()
	if err != nil {
		fmt.Fprintf(stderr, "unverified: %s %s#%s not accessible\n", ghLabel(kind), repo, num)
		return 1
	}

	var v struct {
		State string `json:"state"`
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		fmt.Fprintf(stderr, "unverified: %s %s#%s not accessible\n", ghLabel(kind), repo, num)
		return 1
	}
	fmt.Fprintf(stdout, "verified: %s — %s — %s\n", v.State, v.Title, v.URL)
	return 0
}

func ghLabel(kind string) string {
	if kind == "pr" {
		return "PR"
	}
	return "issue"
}

func verifyFile(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	path := ""
	ref := ""
	if len(args) > 0 {
		path = args[0]
	}
	if len(args) > 1 {
		ref = args[1]
	}
	if path == "" {
		fmt.Fprintln(stderr, "usage: clawctl verify file <path> [<ref>]")
		return 2
	}

	if ref != "" {
		err := exec.CommandContext(ctx, "git", "cat-file", "-e", ref+":"+path).Run()
		if err == nil {
			fmt.Fprintf(stdout, "verified: %s @ %s\n", path, ref)
			return 0
		}
		fmt.Fprintf(stderr, "unverified: %s not present at %s\n", path, ref)
		return 1
	}

	if _, err := os.Lstat(path); err == nil {
		fmt.Fprintf(stdout, "verified: %s (working tree)\n", path)
		return 0
	} else if !errors.Is(err, os.ErrNotExist) {
		// Permission error or similar — bash's `[ -e $path ]` would
		// also report unverified here, so match that.
		fmt.Fprintf(stderr, "unverified: %s not present in working tree\n", path)
		return 1
	}
	fmt.Fprintf(stderr, "unverified: %s not present in working tree\n", path)
	return 1
}

func verifyCmd(cfg config.Config, args []string) {
	code := runVerify(context.Background(), cfg, args, os.Stdout, os.Stderr)
	os.Exit(code)
}
