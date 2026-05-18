package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tomstagl/clawctl/internal/config"
)

func TestTrace_MissingArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runTrace(context.Background(), config.Config{JaegerUI: "http://x"}, nil, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if got := stderr.String(); got != "usage: clawctl trace <trace-id-32-hex>\n" {
		t.Errorf("stderr = %q", got)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}

func TestTrace_MissingJaegerUI(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runTrace(context.Background(), config.Config{}, []string{"abc123"}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	want := "clawctl: CLAWCTL_JAEGER_UI not set. Export your Jaeger base URL (e.g. http://jaeger:16686).\n"
	if got := stderr.String(); got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
}

// expectedSpanLine computes the byte-exact rendered line using the same
// format the bash python heredoc and Go renderer share. Tests use this to
// avoid hand-counting padding.
func expectedSpanLine(svc, op string, durMs float64) string {
	return fmt.Sprintf("  %-24s %-40s %7.0fms\n", svc, op, durMs)
}

func TestTrace_HappyPath(t *testing.T) {
	const tid = "0000000000000000aaaaaaaaaaaaaaaa"
	body := `{
		"data":[{
			"spans":[
				{"operationName":"GET /v1/chat/completions","duration":1500000,"processID":"p1"},
				{"operationName":"agent.run","duration":850000,"processID":"p2"}
			],
			"processes":{
				"p1":{"serviceName":"openclaw-gateway"},
				"p2":{"serviceName":"agent-runner"}
			}
		}]
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "/jaeger/api/traces/" + tid
		if r.URL.Path != want {
			t.Errorf("server got path %q, want %q", r.URL.Path, want)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := runTrace(context.Background(), config.Config{JaegerUI: srv.URL}, []string{tid}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}

	got := stdout.String()
	wantHeader := "trace-id: " + tid + "\nUI:       " + srv.URL + "/trace/" + tid + "\n\n"
	if !strings.HasPrefix(got, wantHeader) {
		t.Errorf("stdout missing header.\n got=%q\nwant prefix=%q", got, wantHeader)
	}

	wantBody := "Spans: 2\n" +
		expectedSpanLine("openclaw-gateway", "GET /v1/chat/completions", 1500) +
		expectedSpanLine("agent-runner", "agent.run", 850)
	if !strings.HasSuffix(got, wantBody) {
		t.Errorf("stdout span block diverges.\n got=%q\nwant suffix=%q", got, wantBody)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestTrace_NoSpans(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := runTrace(context.Background(), config.Config{JaegerUI: srv.URL}, []string{"deadbeef"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Jaeger: no spans for this trace\n") {
		t.Errorf("stdout missing no-spans line: %q", stdout.String())
	}
}

func TestTrace_JaegerErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"msg":"trace not found"}]}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := runTrace(context.Background(), config.Config{JaegerUI: srv.URL}, []string{"deadbeef"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Jaeger: trace not found\n") {
		t.Errorf("stdout missing error line: %q", stdout.String())
	}
}

func TestTrace_JaegerErrorMissingMsg(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{}]}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := runTrace(context.Background(), config.Config{JaegerUI: srv.URL}, []string{"deadbeef"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "Jaeger: unknown\n") {
		t.Errorf("stdout missing fallback msg: %q", stdout.String())
	}
}

func TestTrace_BadJSONStillExitsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<not json>`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := runTrace(context.Background(), config.Config{JaegerUI: srv.URL}, []string{"deadbeef"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (best-effort); stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "trace-id: deadbeef\n") {
		t.Errorf("stdout missing header on bad JSON: %q", stdout.String())
	}
}

func TestTrace_UnreachableStillExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runTrace(context.Background(), config.Config{JaegerUI: "http://127.0.0.1:1"}, []string{"deadbeef"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "trace-id: deadbeef\n") {
		t.Errorf("stdout missing header: %q", stdout.String())
	}
}

func TestTrace_SpanLimit30(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"data":[{"spans":[`)
	for i := 0; i < 35; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"operationName":"op","duration":1000,"processID":"p"}`)
	}
	sb.WriteString(`],"processes":{"p":{"serviceName":"svc"}}}]}`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sb.String()))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := runTrace(context.Background(), config.Config{JaegerUI: srv.URL}, []string{"abc"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}

	out := stdout.String()
	if !strings.Contains(out, "Spans: 35\n") {
		t.Errorf("expected Spans: 35 (count of all spans), got %q", out)
	}
	rendered := strings.Count(out, expectedSpanLine("svc", "op", 1))
	if rendered != 30 {
		t.Errorf("expected 30 rendered spans, got %d (out=%q)", rendered, out)
	}
}

func TestTrace_MissingProcessFallsBackToQuestionMark(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"spans":[{"operationName":"orphan","duration":2000,"processID":"missing"}],"processes":{}}]}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := runTrace(context.Background(), config.Config{JaegerUI: srv.URL}, []string{"abc"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	want := expectedSpanLine("?", "orphan", 2)
	if !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout missing fallback line.\n got=%q\nwant contains=%q", stdout.String(), want)
	}
}

func TestTrace_MissingOperationFallsBackToQuestionMark(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"spans":[{"duration":3000,"processID":"p"}],"processes":{"p":{"serviceName":"svc"}}}]}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := runTrace(context.Background(), config.Config{JaegerUI: srv.URL}, []string{"abc"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	want := expectedSpanLine("svc", "?", 3)
	if !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout missing op-fallback line.\n got=%q\nwant contains=%q", stdout.String(), want)
	}
}
