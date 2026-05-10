package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/tomstagl/clawctl/internal/config"
)

// runTrace implements `clawctl trace <trace-id>`. It mirrors the bash
// subcommand: print a trace-id + UI link header, then best-effort fetch a
// span summary from $CLAWCTL_JAEGER_UI/jaeger/api/traces/<id>. Network or
// parse failures are swallowed so the UI link still surfaces — this matches
// the `trace  best-effort: returns 0 even when Jaeger is unreachable` row in
// `clawctl help`.
//
// Exit codes:
//
//	0   header printed (regardless of Jaeger reachability/parse outcome)
//	2   missing trace-id arg or CLAWCTL_JAEGER_UI unset
func runTrace(ctx context.Context, cfg config.Config, args []string, stdout, stderr io.Writer) int {
	tid := ""
	if len(args) > 0 {
		tid = args[0]
	}
	if tid == "" {
		fmt.Fprintln(stderr, "usage: clawctl trace <trace-id-32-hex>")
		return 2
	}
	if cfg.JaegerUI == "" {
		fmt.Fprintln(stderr, "clawctl: CLAWCTL_JAEGER_UI not set. Export your Jaeger base URL (e.g. http://jaeger:16686).")
		return 2
	}

	fmt.Fprintf(stdout, "trace-id: %s\n", tid)
	fmt.Fprintf(stdout, "UI:       %s/trace/%s\n", cfg.JaegerUI, tid)
	fmt.Fprintln(stdout)

	apiURL := cfg.JaegerUI + "/jaeger/api/traces/" + tid
	body, err := jaegerFetch(ctx, apiURL)
	if err != nil {
		// Bash silently swallows curl/python failures and exits 0; do the
		// same so the UI link remains useful when Jaeger is unreachable.
		return 0
	}
	renderTraceSummary(stdout, body)
	return 0
}

// jaegerFetch issues a 5s-timeout GET against the Jaeger API. The bash version
// uses `curl -sS --max-time 5 ... 2>/dev/null`; we match the per-call ceiling
// while still honouring an upstream context (tests inject their own timeout).
func jaegerFetch(ctx context.Context, url string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// jaegerResponse mirrors the subset of the Jaeger HTTP API the bash python
// heredoc reads: `errors[]`, `data[].spans[]`, `data[].processes`. Anything
// else is ignored.
type jaegerResponse struct {
	Errors []struct {
		Msg string `json:"msg"`
	} `json:"errors"`
	Data []struct {
		Spans     []jaegerSpan              `json:"spans"`
		Processes map[string]jaegerProcess `json:"processes"`
	} `json:"data"`
}

type jaegerSpan struct {
	OperationName string `json:"operationName"`
	Duration      int64  `json:"duration"`
	ProcessID     string `json:"processID"`
}

type jaegerProcess struct {
	ServiceName string `json:"serviceName"`
}

// renderTraceSummary writes the span-summary block to stdout in the exact
// format the bash python heredoc uses:
//
//	Spans: <n>
//	  <svc:24>  <op:40>  <dur:>7.0f>ms
//
// Padding/widths and the ms-rounding formula (microseconds / 1000, rounded
// to integer ms via banker's rounding — both Python's f"{:.0f}" and Go's
// %.0f use round-half-to-even) match byte-for-byte.
func renderTraceSummary(w io.Writer, body []byte) {
	var d jaegerResponse
	if err := json.Unmarshal(body, &d); err != nil {
		return
	}
	if len(d.Errors) > 0 {
		msg := d.Errors[0].Msg
		if msg == "" {
			msg = "unknown"
		}
		fmt.Fprintf(w, "Jaeger: %s\n", msg)
		return
	}
	if len(d.Data) == 0 {
		fmt.Fprintln(w, "Jaeger: no spans for this trace")
		return
	}
	t := d.Data[0]
	fmt.Fprintf(w, "Spans: %d\n", len(t.Spans))
	limit := len(t.Spans)
	if limit > 30 {
		limit = 30
	}
	for _, s := range t.Spans[:limit] {
		op := s.OperationName
		if op == "" {
			op = "?"
		}
		svc := "?"
		if p, ok := t.Processes[s.ProcessID]; ok && p.ServiceName != "" {
			svc = p.ServiceName
		}
		dur := float64(s.Duration) / 1000.0
		fmt.Fprintf(w, "  %-24s %-40s %7.0fms\n", svc, op, dur)
	}
}

func traceCmd(cfg config.Config, args []string) {
	code := runTrace(context.Background(), cfg, args, os.Stdout, os.Stderr)
	os.Exit(code)
}
