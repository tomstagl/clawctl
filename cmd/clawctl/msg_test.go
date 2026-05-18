package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tomstagl/clawctl/internal/config"
	"github.com/tomstagl/clawctl/internal/envelope"
)

// chatCanned is the OpenAI-shape response the mock gateway returns by
// default. Tests can override fields by writing a different body in the
// httptest.HandlerFunc.
const chatCanned = `{
  "id": "chatcmpl-test",
  "object": "chat.completion",
  "model": "openclaw/default",
  "choices": [{
    "index": 0,
    "message": {"role": "assistant", "content": "hello world"},
    "finish_reason": "stop"
  }],
  "usage": {
    "prompt_tokens": 4,
    "completion_tokens": 2,
    "total_tokens": 6
  }
}`

func TestRunMsg_MissingHost(t *testing.T) {
	defer stubTokenSource(t)()
	var stdout, stderr bytes.Buffer
	code := runMsg(context.Background(), config.Config{}, []string{"default", "hi"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "CLAWCTL_HOST not set") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunMsg_MissingAgent(t *testing.T) {
	defer stubTokenSource(t)()
	var stdout, stderr bytes.Buffer
	cfg := config.Config{Host: "http://127.0.0.1:1", Timeout: time.Second}
	code := runMsg(context.Background(), cfg, []string{"-s", "sess"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage: clawctl msg") {
		t.Errorf("stderr = %q, want usage line", stderr.String())
	}
}

func TestRunMsg_UnknownFlag(t *testing.T) {
	defer stubTokenSource(t)()
	var stdout, stderr bytes.Buffer
	cfg := config.Config{Host: "http://127.0.0.1:1", Timeout: time.Second}
	code := runMsg(context.Background(), cfg, []string{"--bogus", "default"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown flag") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunMsg_DefaultEmitsEnvelope(t *testing.T) {
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
		_, _ = w.Write([]byte(chatCanned))
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
	code := runMsg(context.Background(), cfg, []string{"default", "hello"},
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

	// Outbound payload shape: model, stream, messages.
	var sent map[string]any
	if err := json.Unmarshal(seenBody, &sent); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if got := sent["model"]; got != "openclaw/default" {
		t.Errorf("payload.model = %v, want openclaw/default", got)
	}
	if got := sent["stream"]; got != false {
		t.Errorf("payload.stream = %v, want false", got)
	}
	if _, has := sent["user"]; has {
		t.Errorf("payload.user present without -s flag: %v", sent["user"])
	}

	// Envelope on stdout.
	out := stdout.Bytes()
	if !bytes.HasSuffix(out, []byte("\n")) {
		t.Errorf("stdout missing trailing newline")
	}
	if err := envelope.Validate(json.RawMessage(bytes.TrimSpace(out))); err != nil {
		t.Fatalf("envelope.Validate failed: %v\n%s", err, out)
	}

	var got envelope.ToolResponse
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if got.EnvelopeVersion != "1" {
		t.Errorf("envelope_version = %q", got.EnvelopeVersion)
	}
	if got.Kind != "tool_response" {
		t.Errorf("kind = %q", got.Kind)
	}
	if got.Agent != "openclaw/default" {
		t.Errorf("agent = %q", got.Agent)
	}
	if got.Input.Content != "hello" {
		t.Errorf("input.content = %q", got.Input.Content)
	}
	if got.Output != "hello world" {
		t.Errorf("output = %q", got.Output)
	}
	if got.FinishReason != "stop" {
		t.Errorf("finish_reason = %q", got.FinishReason)
	}
	if got.Usage.InputTokens != 4 || got.Usage.OutputTokens != 2 || got.Usage.TotalTokens != 6 {
		t.Errorf("usage = %+v", got.Usage)
	}
	if len(got.Redactions) != 0 {
		t.Errorf("redactions = %+v, want []", got.Redactions)
	}
	// Redactions field must marshal as `[]` (not null) so consumers can branch
	// without nil-checks. Verify by re-marshalling and grepping the output.
	if !bytes.Contains(out, []byte(`"redactions":[]`)) {
		t.Errorf("redactions not serialised as `[]`: %s", out)
	}

	if !strings.Contains(stderr.String(), "trace-id: ") {
		t.Errorf("stderr missing trace-id line: %q", stderr.String())
	}
}

func TestRunMsg_TextFlagEmitsPlainText(t *testing.T) {
	defer stubTokenSource(t)()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(chatCanned))
	}))
	defer srv.Close()

	cfg := config.Config{
		Host:            srv.URL,
		CacheDir:        t.TempDir(),
		KeychainService: "test",
		Timeout:         2 * time.Second,
	}
	var stdout, stderr bytes.Buffer
	code := runMsg(context.Background(), cfg, []string{"--text", "default", "hello"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	if stdout.String() != "hello world\n" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "hello world\n")
	}
	// Confirm it is *not* a JSON envelope.
	if json.Valid(bytes.TrimSpace(stdout.Bytes())) {
		var probe map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &probe); err == nil {
			if _, has := probe["envelope_version"]; has {
				t.Errorf("--text unexpectedly emitted an envelope: %s", stdout.String())
			}
		}
	}
}

func TestRunMsg_SessionFlagFlowsThrough(t *testing.T) {
	defer stubTokenSource(t)()

	var seenBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(chatCanned))
	}))
	defer srv.Close()

	cfg := config.Config{
		Host:            srv.URL,
		CacheDir:        t.TempDir(),
		KeychainService: "test",
		Timeout:         2 * time.Second,
	}
	var stdout, stderr bytes.Buffer
	code := runMsg(context.Background(), cfg, []string{"-s", "sess-42", "default", "hi"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}
	var sent map[string]any
	if err := json.Unmarshal(seenBody, &sent); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if sent["user"] != "sess-42" {
		t.Errorf("payload.user = %v, want sess-42", sent["user"])
	}
	var env envelope.ToolResponse
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.SessionID != "sess-42" {
		t.Errorf("envelope.session_id = %q, want sess-42", env.SessionID)
	}
}

func TestRunMsg_StdinFallback(t *testing.T) {
	defer stubTokenSource(t)()

	var seenBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(chatCanned))
	}))
	defer srv.Close()

	cfg := config.Config{
		Host:            srv.URL,
		CacheDir:        t.TempDir(),
		KeychainService: "test",
		Timeout:         2 * time.Second,
	}
	var stdout, stderr bytes.Buffer
	code := runMsg(context.Background(), cfg, []string{"default"},
		strings.NewReader("piped prompt\n"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}

	var sent map[string]any
	if err := json.Unmarshal(seenBody, &sent); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	msgs := sent["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %v", msgs)
	}
	first := msgs[0].(map[string]any)
	if first["content"] != "piped prompt\n" {
		t.Errorf("messages[0].content = %q, want piped prompt with newline", first["content"])
	}
}

func TestRunMsg_HTTPError(t *testing.T) {
	defer stubTokenSource(t)()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"bad token"}}`))
	}))
	defer srv.Close()

	cfg := config.Config{
		Host:            srv.URL,
		CacheDir:        t.TempDir(),
		KeychainService: "test",
		Timeout:         2 * time.Second,
	}
	var stdout, stderr bytes.Buffer
	code := runMsg(context.Background(), cfg, []string{"default", "hi"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 22 {
		t.Errorf("exit = %d, want 22", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty (errors stay on stderr)", stdout.String())
	}
	if !strings.Contains(stderr.String(), "HTTP 401") {
		t.Errorf("stderr = %q, want HTTP 401", stderr.String())
	}
	if !strings.Contains(stderr.String(), "bad token") {
		t.Errorf("stderr = %q, want decoded gateway message", stderr.String())
	}
}

func TestRunMsg_ConnRefused(t *testing.T) {
	defer stubTokenSource(t)()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close()

	cfg := config.Config{
		Host:            addr,
		CacheDir:        t.TempDir(),
		KeychainService: "test",
		Timeout:         time.Second,
	}
	var stdout, stderr bytes.Buffer
	code := runMsg(context.Background(), cfg, []string{"default", "hi"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 7 {
		t.Errorf("exit = %d, want 7", code)
	}
	if !strings.Contains(stderr.String(), "connection refused") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunMsg_RedactedPathPopulatesRedactions(t *testing.T) {
	defer stubTokenSource(t)()

	// dt0c01 token shape: dt0c01.<>=20+ chars from [A-Za-z0-9_.-]. Embed in
	// the canned content so the redactor picks it up.
	const dt = "dt0c01.SECRETHEAD123.XXXXXXXXXXX"
	resp := strings.Replace(chatCanned, "hello world",
		"see token "+dt+" please", 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(resp))
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
	code := runMsg(context.Background(), cfg, []string{"default", "hi"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}

	var got envelope.ToolResponse
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if strings.Contains(got.Output, dt) {
		t.Errorf("output leaks unredacted secret: %q", got.Output)
	}
	if !strings.Contains(got.Output, "<REDACTED:dt0c01:") {
		t.Errorf("output missing redaction marker: %q", got.Output)
	}
	if len(got.Redactions) != 1 {
		t.Fatalf("redactions = %+v, want exactly 1 hit", got.Redactions)
	}
	if got.Redactions[0].Kind != "dt0c01" {
		t.Errorf("redactions[0].kind = %q, want dt0c01", got.Redactions[0].Kind)
	}
	if got.Redactions[0].OffsetHint == nil {
		t.Errorf("redactions[0].offset_hint = nil, want non-nil pointer")
	}

	// Stderr WARNING should still fire (US-008 contract: in-band redactions[]
	// is additive, the legacy stderr signal stays for human users).
	if !strings.Contains(stderr.String(), "WARNING: redacted secret pattern(s): dt0c01") {
		t.Errorf("stderr missing WARNING line: %q", stderr.String())
	}

	// Audit-file append must land at <CacheDir>/last-redaction.
	audit, err := readFileIfExists(filepath.Join(dir, "last-redaction"))
	if err != nil {
		t.Fatalf("audit read: %v", err)
	}
	if !strings.Contains(audit, "kinds=dt0c01") {
		t.Errorf("audit file = %q, want kinds=dt0c01 entry", audit)
	}
	if !strings.Contains(audit, "agent=default") {
		t.Errorf("audit file = %q, want agent=default", audit)
	}
}

func TestRunMsg_FinishReasonNormalisation(t *testing.T) {
	cases := map[string]string{
		"stop":           "stop",
		"length":         "length",
		"content_filter": "content_filter",
		"error":          "error",
		"tool_calls":     "tool_call",
		"function_call":  "tool_call",
		"tool_call":      "tool_call",
		"":               "stop",
		"weirdo":         "stop",
	}
	for raw, want := range cases {
		if got := mapFinishReason(raw); got != want {
			t.Errorf("mapFinishReason(%q) = %q, want %q", raw, got, want)
		}
	}
}

// readFileIfExists is a tiny helper for the audit-file assertion.
func readFileIfExists(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
