package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/tomstagl/clawctl/internal/cache"
	"github.com/tomstagl/clawctl/internal/config"
	"github.com/tomstagl/clawctl/internal/keychain"
	"github.com/tomstagl/clawctl/internal/transport/api"
)

// runModels implements `clawctl models`. It mirrors the bash _models_cache
// helper byte-for-byte: the body of /v1/models is cached at
// $CLAWCTL_CACHE_DIR/models.json for CLAWCTL_MODELS_TTL seconds, refreshed
// on miss via an authenticated GET, and pretty-printed via `jq .`-style
// indentation. Refresh failures with a pre-existing cache fall back to the
// stale body so a transient gateway outage doesn't break the slug-validation
// dependency wired in around _validate_agent (US-018+).
func runModels(ctx context.Context, cfg config.Config, stdout, stderr io.Writer) int {
	if cfg.Host == "" {
		fmt.Fprintln(stderr, "clawctl: CLAWCTL_HOST not set. Export it (e.g. export CLAWCTL_HOST=http://your-openclaw-host:18789).")
		return 2
	}
	if cfg.CacheDir == "" {
		fmt.Fprintln(stderr, "clawctl: CLAWCTL_CACHE_DIR is empty (HOME unresolved?)")
		return 2
	}

	cachePath := filepath.Join(cfg.CacheDir, "models.json")
	tokenSource := keychainTokenSource(cfg)
	client := api.New(cfg.Host, cfg.Timeout, tokenSource)

	body, err := cache.Get(cachePath, cfg.ModelsTTL, func() ([]byte, error) {
		return client.Get(ctx, "/v1/models", true)
	})
	if err != nil {
		return reportModelsError(cfg, err, stdout, stderr)
	}
	if perr := prettyPrintJSON(stdout, body); perr != nil {
		fmt.Fprintf(stderr, "clawctl: invalid JSON from %s/v1/models: %v\n", cfg.Host, perr)
		return 1
	}
	return 0
}

// keychainTokenSource is the production wiring for an api.TokenSource. It
// reads the bearer token from the Keychain (design principle #2: no env or
// disk fallbacks). Splitting it out lets tests inject a stub directly into
// runModels via overriding the variable below.
var keychainTokenSource = func(cfg config.Config) api.TokenSource {
	return func() (string, error) {
		tok, err := keychain.Token(cfg.KeychainService, "")
		if err != nil {
			return "", err
		}
		return tok, nil
	}
}

func reportModelsError(cfg config.Config, err error, stdout, stderr io.Writer) int {
	var httpErr *api.HTTPError
	if errors.As(err, &httpErr) {
		// Same shape as health's error path: dump the body to stdout for
		// `clawctl models | jq` parity, then exit 22 with a one-line
		// stderr summary.
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
	if errors.Is(err, keychain.ErrNotFound) {
		fmt.Fprintf(stderr, "clawctl: keychain item %q not found. Add a token with: security add-generic-password -s %s -a $USER -w\n",
			cfg.KeychainService, cfg.KeychainService)
		return 2
	}
	fmt.Fprintf(stderr, "clawctl: %v\n", err)
	return api.ExitCode(err)
}

// modelsCmd is the entry-point wrapper used by main(). Threads
// os.Stdout / os.Stderr and exits with the documented code.
func modelsCmd(cfg config.Config) {
	code := runModels(context.Background(), cfg, os.Stdout, os.Stderr)
	os.Exit(code)
}
