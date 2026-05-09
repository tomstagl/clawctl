package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/tomstagl/clawctl/internal/config"
	"github.com/tomstagl/clawctl/internal/transport/api"
)

// runHealth implements `clawctl health`. It mirrors the bash subcommand
// (curl --silent --show-error --fail-with-body --max-time TIMEOUT --retry 2
// ${CLAWCTL_HOST}/health | jq .) and surfaces the same documented exit
// codes (0/2/6/7/22/28). The /health endpoint is anonymous on the gateway
// so no Keychain access is needed — Token is left nil.
func runHealth(ctx context.Context, cfg config.Config, stdout, stderr io.Writer) int {
	if cfg.Host == "" {
		fmt.Fprintln(stderr, "clawctl: CLAWCTL_HOST not set. Export it (e.g. export CLAWCTL_HOST=http://your-openclaw-host:18789).")
		return 2
	}

	client := api.New(cfg.Host, cfg.Timeout, nil)
	body, err := client.Get(ctx, "/health", false)
	if err != nil {
		return reportHealthError(cfg, err, stdout, stderr)
	}
	if perr := prettyPrintJSON(stdout, body); perr != nil {
		fmt.Fprintf(stderr, "clawctl: invalid JSON from %s/health: %v\n", cfg.Host, perr)
		return 1
	}
	return 0
}

func reportHealthError(cfg config.Config, err error, stdout, stderr io.Writer) int {
	var httpErr *api.HTTPError
	if errors.As(err, &httpErr) {
		// Mirror bash: curl --fail-with-body still surfaces the body to
		// stdout, then exits 22. We pretty-print the body if it's JSON so
		// `clawctl health | jq` parity holds for the success and error
		// paths alike.
		if perr := prettyPrintJSON(stdout, httpErr.Body); perr != nil {
			_, _ = stdout.Write(httpErr.Body)
			if len(httpErr.Body) > 0 && httpErr.Body[len(httpErr.Body)-1] != '\n' {
				_, _ = stdout.Write([]byte{'\n'})
			}
		}
		fmt.Fprintf(stderr, "clawctl: gateway error: HTTP %d\n", httpErr.StatusCode)
		return 22
	}
	var dnsErr *api.DNSError
	if errors.As(err, &dnsErr) {
		fmt.Fprintf(stderr, "clawctl: DNS resolution failed for %s\n", cfg.Host)
		return 6
	}
	var refErr *api.ConnRefusedError
	if errors.As(err, &refErr) {
		fmt.Fprintf(stderr, "clawctl: connection refused: %s\n", cfg.Host)
		return 7
	}
	var toErr *api.TimeoutError
	if errors.As(err, &toErr) {
		fmt.Fprintf(stderr, "clawctl: timeout (%ds) calling %s\n", int(cfg.Timeout.Seconds()), cfg.Host)
		return 28
	}
	fmt.Fprintf(stderr, "clawctl: %v\n", err)
	return api.ExitCode(err)
}

// prettyPrintJSON formats body the same way `jq .` does: 2-space indent,
// preserve numeric precision, no HTML escaping, trailing newline. We keep
// numbers as json.Number so floats like 1.0 don't get re-rendered as 1.
func prettyPrintJSON(w io.Writer, body []byte) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// healthCmd is the entry-point wrapper used by main(). It threads
// os.Stdout / os.Stderr and exits with the documented code.
func healthCmd(cfg config.Config) {
	code := runHealth(context.Background(), cfg, os.Stdout, os.Stderr)
	os.Exit(code)
}
