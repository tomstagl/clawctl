package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tomstagl/clawctl/internal/config"
	"github.com/tomstagl/clawctl/internal/envelope"
)

// canonicalSSE is the recorded SSE response the byte-exact NDJSON
// fixture below was generated against. Three content deltas, then a
// finish frame carrying usage, then the OpenAI [DONE] sentinel.
const canonicalSSE = "" +
	`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"Hello "}}]}` + "\n\n" +
	`data: {"choices":[{"index":0,"delta":{"content":"streamed "}}]}` + "\n\n" +
	`data: {"choices":[{"index":0,"delta":{"content":"world."}}]}` + "\n\n" +
	`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}` + "\n\n" +
	`data: [DONE]` + "\n\n"

func TestRunStream_MissingHost(t *testing.T) {
	defer stubTokenSource(t)()
	var stdout, stderr bytes.Buffer
	code := runStream(context.Background(), config.Config{}, []string{"default", "hi"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "CLAWCTL_HOST not set") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunStream_MissingAgent(t *testing.T) {
	defer stubTokenSource(t)()
	var stdout, stderr bytes.Buffer
	cfg := config.Config{Host: "http://127.0.0.1:1", Timeout: time.Second}
	code := runStream(context.Background(), cfg, []string{"-s", "sess"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage: clawctl stream") {
		t.Errorf("stderr = %q, want usage line", stderr.String())
	}
}

func TestRunStream_UnknownFlag(t *testing.T) {
	defer stubTokenSource(t)()
	var stdout, stderr bytes.Buffer
	cfg := config.Config{Host: "http://127.0.0.1:1", Timeout: time.Second}
	code := runStream(context.Background(), cfg, []string{"--bogus", "default"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown flag") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunStream_DefaultEmitsNDJSONEnvelope(t *testing.T) {
	defer stubTokenSource(t)()

	var seenAuth, seenTP, seenCT string
	var seenBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		seenAuth = r.Header.Get("Authorization")
		seenTP = r.Header.Get("traceparent")
		seenCT = r.Header.Get("Content-Type")
		seenBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(canonicalSSE))
	}))
	defer srv.Close()

	cfg := config.Config{
		Host:            srv.URL,
		CacheDir:        t.TempDir(),
		KeychainService: "test",
		Timeout:         2 * time.Second,
	}
	var stdout, stderr bytes.Buffer
	code := runStream(context.Background(), cfg, []string{"default", "say hi"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}

	if seenAuth != "Bearer tok-test" {
		t.Errorf("Authorization = %q", seenAuth)
	}
	if seenCT != "application/json" {
		t.Errorf("Content-Type = %q", seenCT)
	}
	if !regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`).MatchString(seenTP) {
		t.Errorf("traceparent = %q, want W3C shape", seenTP)
	}

	// Outbound payload: model, stream=true, messages.
	var sent map[string]any
	if err := json.Unmarshal(seenBody, &sent); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if sent["model"] != "openclaw/default" {
		t.Errorf("payload.model = %v", sent["model"])
	}
	if sent["stream"] != true {
		t.Errorf("payload.stream = %v, want true", sent["stream"])
	}

	lines := splitNDJSON(t, stdout.Bytes())
	if len(lines) != 4 {
		t.Fatalf("got %d NDJSON lines, want 4 (3 chunks + 1 response):\n%s", len(lines), stdout.String())
	}

	// Each line must validate against the v1 schema.
	for i, line := range lines {
		var doc any
		if err := json.Unmarshal(line, &doc); err != nil {
			t.Fatalf("line %d: parse: %v", i, err)
		}
		if err := envelope.Validate(doc); err != nil {
			t.Fatalf("line %d: validate: %v\n  raw=%s", i, err, line)
		}
	}

	// Lines 0..2 are tool_stream_chunk with strictly increasing indices and
	// finish_reason explicitly null.
	chunks := make([]envelope.ToolStreamChunk, 3)
	for i := 0; i < 3; i++ {
		if err := json.Unmarshal(lines[i], &chunks[i]); err != nil {
			t.Fatalf("unmarshal chunk %d: %v", i, err)
		}
		if chunks[i].Kind != envelope.KindToolStreamChunk {
			t.Errorf("chunk %d kind = %q", i, chunks[i].Kind)
		}
		if chunks[i].Index != i {
			t.Errorf("chunk %d index = %d, want %d", i, chunks[i].Index, i)
		}
		if chunks[i].FinishReason != nil {
			t.Errorf("chunk %d finish_reason = %v, want nil", i, chunks[i].FinishReason)
		}
		if chunks[i].Agent != "openclaw/default" {
			t.Errorf("chunk %d agent = %q", i, chunks[i].Agent)
		}
	}

	// Schema requires `"finish_reason":null` to appear literally on chunks.
	for i := 0; i < 3; i++ {
		if !bytes.Contains(lines[i], []byte(`"finish_reason":null`)) {
			t.Errorf("chunk %d missing literal `\"finish_reason\":null`: %s", i, lines[i])
		}
	}

	// Content reassembles to the gateway-emitted aggregate.
	var assembled string
	for _, c := range chunks {
		assembled += c.Delta.Content
	}
	if assembled != "Hello streamed world." {
		t.Errorf("assembled chunk content = %q", assembled)
	}

	// Last line is the terminal ToolResponse.
	var resp envelope.ToolResponse
	if err := json.Unmarshal(lines[3], &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Kind != envelope.KindToolResponse {
		t.Errorf("response.kind = %q", resp.Kind)
	}
	if resp.Output != "Hello streamed world." {
		t.Errorf("response.output = %q", resp.Output)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("response.finish_reason = %q", resp.FinishReason)
	}
	if resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 3 || resp.Usage.TotalTokens != 10 {
		t.Errorf("response.usage = %+v", resp.Usage)
	}
	if resp.Input.Content != "say hi" {
		t.Errorf("response.input.content = %q", resp.Input.Content)
	}

	// Single traceparent shared across every emitted line.
	tp := chunks[0].Traceparent
	if !regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`).MatchString(tp) {
		t.Errorf("chunk traceparent = %q", tp)
	}
	for _, c := range chunks {
		if c.Traceparent != tp {
			t.Errorf("traceparent drift across chunks: %q != %q", c.Traceparent, tp)
		}
	}
	if resp.Traceparent != tp {
		t.Errorf("response.traceparent = %q, chunks = %q", resp.Traceparent, tp)
	}

	// trace-id surfaces on stderr (design principle 3).
	if !strings.Contains(stderr.String(), "trace-id: ") {
		t.Errorf("stderr missing trace-id: %q", stderr.String())
	}
}

func TestRunStream_ByteExactNDJSON(t *testing.T) {
	// Self-parity golden: render the canonical SSE response through the
	// production code path, then compare against a templated golden
	// string with the call's traceparent substituted in. Anything other
	// than the traceparent (chunk content, redactions shape, terminal
	// envelope) must reproduce byte-for-byte across runs.
	defer stubTokenSource(t)()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(canonicalSSE))
	}))
	defer srv.Close()

	cfg := config.Config{
		Host:            srv.URL,
		CacheDir:        t.TempDir(),
		KeychainService: "test",
		Timeout:         2 * time.Second,
	}
	var stdout, stderr bytes.Buffer
	code := runStream(context.Background(), cfg, []string{"default", "say hi"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}

	// Pull the actual traceparent off the first emitted line so we can
	// substitute it into the golden template.
	first := bytes.SplitN(stdout.Bytes(), []byte("\n"), 2)[0]
	var probe envelope.ToolStreamChunk
	if err := json.Unmarshal(first, &probe); err != nil {
		t.Fatalf("first line not a chunk: %v\n%s", err, first)
	}
	tp := probe.Traceparent

	const tpl = `{"envelope_version":"1","kind":"tool_stream_chunk","agent":"openclaw/default","task_id":"__TASK__","traceparent":"__TP__","index":0,"delta":{"content":"Hello "},"finish_reason":null}
{"envelope_version":"1","kind":"tool_stream_chunk","agent":"openclaw/default","task_id":"__TASK__","traceparent":"__TP__","index":1,"delta":{"content":"streamed "},"finish_reason":null}
{"envelope_version":"1","kind":"tool_stream_chunk","agent":"openclaw/default","task_id":"__TASK__","traceparent":"__TP__","index":2,"delta":{"content":"world."},"finish_reason":null}
{"envelope_version":"1","kind":"tool_response","agent":"openclaw/default","task_id":"__TASK__","traceparent":"__TP__","input":{"role":"user","content":"say hi"},"output":"Hello streamed world.","redactions":[],"usage":{"input_tokens":7,"output_tokens":3,"total_tokens":10},"finish_reason":"stop"}
`
	want := strings.ReplaceAll(tpl, "__TP__", tp)
	want = strings.ReplaceAll(want, "__TASK__", probe.TaskID)
	if got := stdout.String(); got != want {
		t.Errorf("byte-exact NDJSON mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRunStream_TextFlagEmitsBufferedPlainText(t *testing.T) {
	defer stubTokenSource(t)()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(canonicalSSE))
	}))
	defer srv.Close()

	cfg := config.Config{
		Host:            srv.URL,
		CacheDir:        t.TempDir(),
		KeychainService: "test",
		Timeout:         2 * time.Second,
	}
	var stdout, stderr bytes.Buffer
	code := runStream(context.Background(), cfg, []string{"--text", "default", "say hi"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	// Plain text: aggregate content + trailing '\n'. Must NOT be JSON.
	if stdout.String() != "Hello streamed world.\n" {
		t.Errorf("stdout = %q", stdout.String())
	}
	if json.Valid(bytes.TrimSpace(stdout.Bytes())) {
		t.Errorf("stdout unexpectedly parses as JSON: %s", stdout.String())
	}
}

func TestRunStream_SessionFlagFlowsThrough(t *testing.T) {
	defer stubTokenSource(t)()

	var seenBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(canonicalSSE))
	}))
	defer srv.Close()

	cfg := config.Config{
		Host:            srv.URL,
		CacheDir:        t.TempDir(),
		KeychainService: "test",
		Timeout:         2 * time.Second,
	}
	var stdout, stderr bytes.Buffer
	code := runStream(context.Background(), cfg, []string{"-s", "sess-7", "default", "hi"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}
	var sent map[string]any
	if err := json.Unmarshal(seenBody, &sent); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if sent["user"] != "sess-7" {
		t.Errorf("payload.user = %v, want sess-7", sent["user"])
	}
	// Every emitted envelope must carry the session_id.
	for i, line := range splitNDJSON(t, stdout.Bytes()) {
		var probe map[string]any
		if err := json.Unmarshal(line, &probe); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		if probe["session_id"] != "sess-7" {
			t.Errorf("line %d session_id = %v, want sess-7", i, probe["session_id"])
		}
	}
}

func TestRunStream_StdinFallback(t *testing.T) {
	defer stubTokenSource(t)()

	var seenBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(canonicalSSE))
	}))
	defer srv.Close()

	cfg := config.Config{
		Host:            srv.URL,
		CacheDir:        t.TempDir(),
		KeychainService: "test",
		Timeout:         2 * time.Second,
	}
	var stdout, stderr bytes.Buffer
	code := runStream(context.Background(), cfg, []string{"default"},
		strings.NewReader("piped prompt\n"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}
	var sent map[string]any
	if err := json.Unmarshal(seenBody, &sent); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	msgs := sent["messages"].([]any)
	first := msgs[0].(map[string]any)
	if first["content"] != "piped prompt\n" {
		t.Errorf("messages[0].content = %q", first["content"])
	}
}

func TestRunStream_RedactedChunkPopulatesRedactions(t *testing.T) {
	defer stubTokenSource(t)()

	// Embed a dt0c01-shape token entirely inside the *middle* delta so
	// per-chunk redaction handles it without crossing a boundary; the
	// redactions[] array should appear on the second chunk and on the
	// terminal ToolResponse, not on chunks 0 or 2.
	const dt = "dt0c01.SECRETHEAD123.XXXXXXXXXXX"
	sse := "" +
		`data: {"choices":[{"index":0,"delta":{"content":"prefix "}}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{"content":"see ` + dt + ` end"}}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{"content":" suffix"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}` + "\n\n" +
		`data: [DONE]` + "\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sse))
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := config.Config{
		Host:            srv.URL,
		CacheDir:        dir,
		KeychainService: "test",
		Timeout:         2 * time.Second,
	}
	var stdout, stderr bytes.Buffer
	code := runStream(context.Background(), cfg, []string{"default", "hi"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}

	lines := splitNDJSON(t, stdout.Bytes())
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4: %s", len(lines), stdout.String())
	}

	// Output must not leak the secret anywhere.
	for i, line := range lines {
		if bytes.Contains(line, []byte(dt)) {
			t.Errorf("line %d leaks unredacted secret: %s", i, line)
		}
	}

	// Chunk 1 carries the dt0c01 redaction; chunks 0 and 2 do not.
	var c0, c1, c2 envelope.ToolStreamChunk
	if err := json.Unmarshal(lines[0], &c0); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(lines[1], &c1); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(lines[2], &c2); err != nil {
		t.Fatal(err)
	}
	if len(c0.Redactions) != 0 {
		t.Errorf("c0.Redactions = %+v, want empty", c0.Redactions)
	}
	if len(c1.Redactions) != 1 || c1.Redactions[0].Kind != "dt0c01" {
		t.Errorf("c1.Redactions = %+v, want one dt0c01 hit", c1.Redactions)
	}
	if len(c2.Redactions) != 0 {
		t.Errorf("c2.Redactions = %+v, want empty", c2.Redactions)
	}

	// Terminal ToolResponse aggregates the redaction into redactions[].
	var resp envelope.ToolResponse
	if err := json.Unmarshal(lines[3], &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Redactions) != 1 || resp.Redactions[0].Kind != "dt0c01" {
		t.Errorf("resp.Redactions = %+v", resp.Redactions)
	}
	if !strings.Contains(resp.Output, "<REDACTED:dt0c01:") {
		t.Errorf("resp.Output missing redaction marker: %q", resp.Output)
	}

	// Stderr WARNING + audit-file append still fire (US-008 contract).
	if !strings.Contains(stderr.String(), "WARNING: redacted secret pattern(s): dt0c01") {
		t.Errorf("stderr missing WARNING: %q", stderr.String())
	}
	audit, err := readFileIfExists(filepath.Join(dir, "last-redaction"))
	if err != nil {
		t.Fatalf("audit read: %v", err)
	}
	if !strings.Contains(audit, "kinds=dt0c01") {
		t.Errorf("audit file missing dt0c01 entry: %q", audit)
	}
}

func TestRunStream_BoundaryCrossingSecretCoalesces(t *testing.T) {
	defer stubTokenSource(t)()

	// Split a dt0c01 token across two SSE deltas: per-chunk redaction
	// can't see it, but the aggregate pass does. The emitter should
	// collapse to a single ToolStreamChunk carrying the boundary-safe
	// redacted aggregate, then emit the terminal ToolResponse.
	sse := "" +
		`data: {"choices":[{"index":0,"delta":{"content":"head dt0c01.SECRETH"}}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{"content":"EAD123.XXXXXXXXXXX tail"}}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{}}` + "\n\n" +
		`data: [DONE]` + "\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sse))
	}))
	defer srv.Close()

	cfg := config.Config{
		Host:            srv.URL,
		CacheDir:        t.TempDir(),
		KeychainService: "test",
		Timeout:         2 * time.Second,
	}
	var stdout, stderr bytes.Buffer
	code := runStream(context.Background(), cfg, []string{"default", "hi"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}

	lines := splitNDJSON(t, stdout.Bytes())
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2 (1 coalesced chunk + 1 response): %s", len(lines), stdout.String())
	}

	var coalesced envelope.ToolStreamChunk
	if err := json.Unmarshal(lines[0], &coalesced); err != nil {
		t.Fatal(err)
	}
	if coalesced.Index != 0 {
		t.Errorf("coalesced.Index = %d", coalesced.Index)
	}
	if !strings.Contains(coalesced.Delta.Content, "<REDACTED:dt0c01:") {
		t.Errorf("coalesced delta missing redaction marker: %q", coalesced.Delta.Content)
	}
	if strings.Contains(coalesced.Delta.Content, "dt0c01.SECRETH") {
		t.Errorf("coalesced delta leaks split secret prefix: %q", coalesced.Delta.Content)
	}
	if len(coalesced.Redactions) != 1 || coalesced.Redactions[0].Kind != "dt0c01" {
		t.Errorf("coalesced.Redactions = %+v", coalesced.Redactions)
	}

	if !strings.Contains(stderr.String(), "redacted secret pattern crossed SSE chunk boundary") {
		t.Errorf("stderr missing boundary-coalesce warning: %q", stderr.String())
	}
}

func TestRunStream_HTTPError(t *testing.T) {
	defer stubTokenSource(t)()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limited","message":"slow down"}}`))
	}))
	defer srv.Close()

	cfg := config.Config{
		Host:            srv.URL,
		CacheDir:        t.TempDir(),
		KeychainService: "test",
		Timeout:         2 * time.Second,
	}
	var stdout, stderr bytes.Buffer
	code := runStream(context.Background(), cfg, []string{"default", "hi"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 22 {
		t.Errorf("exit = %d, want 22", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty (errors stay on stderr)", stdout.String())
	}
	if !strings.Contains(stderr.String(), "HTTP 429") {
		t.Errorf("stderr = %q, want HTTP 429", stderr.String())
	}
}

// splitNDJSON splits a buffer at '\n' and drops trailing empty lines.
// Returns the per-line byte slices unchanged so JSON parsing can pick
// them up without re-encoding.
func splitNDJSON(t *testing.T, b []byte) [][]byte {
	t.Helper()
	parts := bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n"))
	out := make([][]byte, 0, len(parts))
	for _, p := range parts {
		if len(p) == 0 {
			continue
		}
		out = append(out, p)
	}
	return out
}

// TestParseSSEStream_SkippedCountsMalformed verifies that malformed data
// payloads are counted (so the caller can warn) rather than silently dropped,
// and that a fully-corrupt stream is distinguishable from a healthy empty one.
func TestParseSSEStream_SkippedCountsMalformed(t *testing.T) {
	body := "data: {not valid json\n\n" +
		"data: also <broken>\n\n" +
		"data: [DONE]\n\n"
	res, err := parseSSEStream([]byte(body))
	if err != nil {
		t.Fatalf("parseSSEStream: %v", err)
	}
	if res.Skipped != 2 {
		t.Errorf("Skipped = %d, want 2", res.Skipped)
	}
	if len(res.Chunks) != 0 {
		t.Errorf("Chunks = %v, want none", res.Chunks)
	}
}

// TestParseSSEStream_CleanStreamNoSkips verifies the counter stays zero for a
// well-formed stream.
func TestParseSSEStream_CleanStreamNoSkips(t *testing.T) {
	body := `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n" +
		"data: [DONE]\n\n"
	res, err := parseSSEStream([]byte(body))
	if err != nil {
		t.Fatalf("parseSSEStream: %v", err)
	}
	if res.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0", res.Skipped)
	}
	if len(res.Chunks) != 1 || res.Chunks[0] != "hi" {
		t.Errorf("Chunks = %v, want [hi]", res.Chunks)
	}
}
