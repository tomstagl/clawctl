package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/tomstagl/clawctl/internal/config"
	"github.com/tomstagl/clawctl/internal/keychain"
	"github.com/tomstagl/clawctl/internal/logging"
	"github.com/tomstagl/clawctl/internal/trace"
	"github.com/tomstagl/clawctl/internal/transport/api"
)

// runRaw implements `clawctl raw METHOD PATH [-d BODY] [-H HEADER]…`. It
// mirrors the bash entrypoint: a fresh W3C traceparent on every call,
// trace-id printed to stderr, body emitted unmodified to stdout, and curl-
// aligned exit codes (0/2/6/7/22/28). Retries are applied only when the verb
// is GET — the bash version sets --retry-all-errors only for GETs because
// retrying a non-idempotent POST risks duplicate side effects.
//
// Flag surface is intentionally narrow: -d/--data and -H/--header. The bash
// version forwards all curl flags via "$@", but a typed binary that
// reimplements every curl flag is the wrong abstraction. Common parity cases
// (GET, POST with a body, error responses) are covered with these two flags;
// users with niche curl needs should fall back to the bash entrypoint.
func runRaw(ctx context.Context, cfg config.Config, args []string, stdout, stderr io.Writer) (code int) {
	log := logging.New(cfg.Log, stderr, "raw", logging.TransportAPI)
	defer func() { code = log.Finish(code) }()
	stderr = log.Stderr()

	if cfg.Host == "" {
		fmt.Fprintln(stderr, "clawctl: CLAWCTL_HOST not set. Export it (e.g. export CLAWCTL_HOST=http://your-openclaw-host:18789).")
		return 2
	}

	method := http.MethodGet
	path := "/health"
	if len(args) > 0 {
		method = strings.ToUpper(args[0])
	}
	if len(args) > 1 {
		path = args[1]
	}
	rest := []string{}
	if len(args) > 2 {
		rest = args[2:]
	}

	var body []byte
	var extraHeaders []string
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "-d" || a == "--data":
			if i+1 >= len(rest) {
				fmt.Fprintf(stderr, "clawctl raw: %s requires an argument\n", a)
				return 2
			}
			body = []byte(rest[i+1])
			i++
		case strings.HasPrefix(a, "--data="):
			body = []byte(strings.TrimPrefix(a, "--data="))
		case a == "-H" || a == "--header":
			if i+1 >= len(rest) {
				fmt.Fprintf(stderr, "clawctl raw: %s requires an argument\n", a)
				return 2
			}
			extraHeaders = append(extraHeaders, rest[i+1])
			i++
		case strings.HasPrefix(a, "--header="):
			extraHeaders = append(extraHeaders, strings.TrimPrefix(a, "--header="))
		default:
			fmt.Fprintf(stderr, "clawctl raw: unsupported flag %q (typed binary supports -d/--data and -H/--header only; use the bash entrypoint for arbitrary curl flags)\n", a)
			return 2
		}
	}

	tp, err := trace.New()
	if err != nil {
		fmt.Fprintf(stderr, "clawctl: %v\n", err)
		return 1
	}
	log.SetTraceparent(tp.String())
	fmt.Fprintf(stderr, "trace-id: %s\n", tp.TraceID)

	tokenSource := keychainTokenSource(cfg)
	client := api.New(cfg.Host, cfg.Timeout, tokenSource)

	respBody, err := client.Do(ctx, api.Request{
		Method:      method,
		Path:        path,
		Body:        body,
		Authed:      true,
		Traceparent: tp.String(),
		Headers:     extraHeaders,
		Retry:       method == http.MethodGet,
	})
	if err != nil {
		return reportRawError(cfg, err, stdout, stderr)
	}
	_, _ = stdout.Write(respBody)
	return 0
}

// reportRawError mirrors the bash entrypoint: HTTP errors emit the body
// verbatim to stdout (as curl --fail-with-body does, no pretty-printing) and
// exit 22; transport classes map to the documented exit codes.
func reportRawError(cfg config.Config, err error, stdout, stderr io.Writer) int {
	var httpErr *api.HTTPError
	if errors.As(err, &httpErr) {
		_, _ = stdout.Write(httpErr.Body)
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
	if errors.Is(err, keychain.ErrNotFound) {
		fmt.Fprintf(stderr, "clawctl: keychain item %q not found. Add a token with: security add-generic-password -s %s -a $USER -w\n",
			cfg.KeychainService, cfg.KeychainService)
		return 2
	}
	fmt.Fprintf(stderr, "clawctl: %v\n", err)
	return api.ExitCode(err)
}

// rawCmd is the entry-point wrapper used by main(). Threads os.Stdout /
// os.Stderr and exits with the documented code.
func rawCmd(cfg config.Config, args []string) {
	code := runRaw(context.Background(), cfg, args, os.Stdout, os.Stderr)
	os.Exit(code)
}
