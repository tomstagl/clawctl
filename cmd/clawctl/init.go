package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/tomstagl/clawctl/internal/config"
	"github.com/tomstagl/clawctl/internal/transport/api"
)

// initCheckTokenSource and initCheckNewClient are package-level factories so
// tests can inject stubs without touching production paths.
var initCheckTokenSource = keychainTokenSource

var initCheckNewClient = func(host string, timeout time.Duration) *api.Client {
	c := api.New(host, timeout, nil)
	c.Retries = 0
	return c
}

// initCmd is the entry-point wrapper used by main().
func initCmd(cfg config.Config, args []string) {
	for _, a := range args {
		if a == "--check" {
			code := runInitCheck(context.Background(), cfg, os.Stdout, os.Stderr)
			os.Exit(code)
		}
	}
	runInitSnippets(os.Stdout, isTTY(os.Stdout))
	os.Exit(0)
}

// isTTY reports whether f is connected to an interactive terminal.
func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// runInitSnippets prints platform-correct copy-pasteable setup instructions.
func runInitSnippets(w io.Writer, color bool) {
	bold := func(s string) string {
		if color {
			return "\x1b[1m" + s + "\x1b[0m"
		}
		return s
	}
	svc := "openclaw-gateway-token"

	if runtime.GOOS == "darwin" {
		fmt.Fprintln(w, bold("Setup (macOS Keychain):"))
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  Store your bearer token:")
		fmt.Fprintf(w, "    security add-generic-password -s %s -a $USER -w\n", svc)
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  Set the gateway URL:")
		fmt.Fprintln(w, "    export CLAWCTL_HOST=http://your-openclaw-host:18789")
	} else {
		fmt.Fprintln(w, bold("Setup (Linux):"))
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  Option 1 — CLAWCTL_TOKEN_CMD (recommended):")
		fmt.Fprintln(w, "    export CLAWCTL_TOKEN_CMD=\"cat ~/.config/clawctl/token\"")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  Option 2 — secret-tool (GNOME Keyring):")
		fmt.Fprintf(w, "    secret-tool store --label='openclaw gateway token' service %s account $USER\n", svc)
		fmt.Fprintf(w, "    export CLAWCTL_TOKEN_CMD=\"secret-tool lookup service %s account $USER\"\n", svc)
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  Option 3 — pass:")
		fmt.Fprintln(w, "    pass insert openclaw/gateway-token")
		fmt.Fprintln(w, "    export CLAWCTL_TOKEN_CMD=\"pass show openclaw/gateway-token\"")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  Set the gateway URL:")
		fmt.Fprintln(w, "    export CLAWCTL_HOST=http://your-openclaw-host:18789")
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Verify your setup:")
	fmt.Fprintln(w, "    clawctl init --check")
}

const (
	checkPass = "[ OK ]"
	checkFail = "[FAIL]"
	checkSkip = "[SKIP]"
)

// runInitCheck runs three ordered environment checks and prints a per-check
// result line to stdout. Returns 0 only if all three pass; returns 2 otherwise.
func runInitCheck(ctx context.Context, cfg config.Config, stdout, _ io.Writer) int {
	// Check 1: CLAWCTL_HOST
	if cfg.Host == "" {
		fmt.Fprintln(stdout, checkFail+" CLAWCTL_HOST: not set")
		fmt.Fprintln(stdout, "       hint: export CLAWCTL_HOST=http://your-openclaw-host:18789")
		fmt.Fprintln(stdout, checkSkip+" token resolver (CLAWCTL_HOST not set)")
		fmt.Fprintln(stdout, checkSkip+" health endpoint (CLAWCTL_HOST not set)")
		return 2
	}
	fmt.Fprintf(stdout, "%s CLAWCTL_HOST: %s\n", checkPass, cfg.Host)

	failed := false

	// Check 2: token resolver
	tokenSrc := initCheckTokenSource(cfg)
	tok, err := tokenSrc()
	if err != nil {
		fmt.Fprintf(stdout, "%s token resolver: %v\n", checkFail, err)
		failed = true
	} else if tok == "" {
		fmt.Fprintln(stdout, checkFail+" token resolver: empty token returned")
		failed = true
	} else {
		fmt.Fprintln(stdout, checkPass+" token resolver: token retrieved")
	}

	// Check 3: health (unauthenticated — always attempted when host is set)
	client := initCheckNewClient(cfg.Host, cfg.Timeout)
	_, herr := client.Get(ctx, "/health", false)
	if herr != nil {
		fmt.Fprintf(stdout, "%s health: %v\n", checkFail, herr)
		failed = true
	} else {
		fmt.Fprintln(stdout, checkPass+" health: HTTP 200")
	}

	if failed {
		return 2
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "All checks passed. clawctl is configured correctly.")
	return 0
}
