package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomstagl/clawctl/internal/config"
)

// decodeEnvelope unmarshals the JSON envelope written to stdout.
func decodeEnvelope(t *testing.T, out string) jsonEnvelope {
	t.Helper()
	var env jsonEnvelope
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &env); err != nil {
		t.Fatalf("stdout is not a valid JSON envelope: %v\nstdout=%q", err, out)
	}
	return env
}

// ── health ──────────────────────────────────────────────────────────────────

func TestRunHealth_JSON_Happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	cfg := config.Config{Host: srv.URL, Timeout: 2 * time.Second, JSONOutput: true}
	code := runHealth(context.Background(), cfg, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	env := decodeEnvelope(t, stdout.String())
	if env.Command != "health" {
		t.Errorf("command = %q, want %q", env.Command, "health")
	}
	if !env.Ok {
		t.Errorf("ok = false, want true")
	}
	if env.Error != nil {
		t.Errorf("error = %v, want nil", env.Error)
	}
	// data should contain the server response
	var data map[string]any
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("data is not JSON: %v", err)
	}
	if data["status"] != "ok" {
		t.Errorf("data.status = %v, want ok", data["status"])
	}
}

func TestRunHealth_JSON_Error(t *testing.T) {
	// Closed server → connection refused → exit 7.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close()

	var stdout, stderr bytes.Buffer
	cfg := config.Config{Host: addr, Timeout: 1 * time.Second, JSONOutput: true}
	code := runHealth(context.Background(), cfg, &stdout, &stderr)
	if code != 7 {
		t.Fatalf("exit = %d, want 7", code)
	}
	env := decodeEnvelope(t, stdout.String())
	if env.Command != "health" {
		t.Errorf("command = %q, want %q", env.Command, "health")
	}
	if env.Ok {
		t.Errorf("ok = true, want false")
	}
	if env.Error == nil {
		t.Fatal("error is nil, want non-nil")
	}
	if env.Error.Code != "conn_refused" {
		t.Errorf("error.code = %q, want conn_refused", env.Error.Code)
	}
	if env.Error.Message == "" {
		t.Error("error.message is empty")
	}
}

// ── models ───────────────────────────────────────────────────────────────────

func TestRunModels_JSON_Happy(t *testing.T) {
	const modelsBody = `{"object":"list","data":[{"id":"openclaw/default"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(modelsBody))
	}))
	defer srv.Close()

	dir := t.TempDir()
	defer stubTokenSource(t)()

	var stdout, stderr bytes.Buffer
	cfg := config.Config{
		Host:      srv.URL,
		Timeout:   2 * time.Second,
		CacheDir:  dir,
		ModelsTTL: time.Second,
		JSONOutput: true,
	}
	code := runModels(context.Background(), cfg, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	env := decodeEnvelope(t, stdout.String())
	if env.Command != "models" {
		t.Errorf("command = %q, want models", env.Command)
	}
	if !env.Ok {
		t.Errorf("ok = false, want true")
	}
	if env.Error != nil {
		t.Errorf("error = %v, want nil", env.Error)
	}
	// data should contain the models response
	var data map[string]any
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("data is not JSON: %v", err)
	}
	if data["object"] != "list" {
		t.Errorf("data.object = %v, want list", data["object"])
	}
}

func TestRunModels_JSON_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	defer stubTokenSource(t)()

	var stdout, stderr bytes.Buffer
	cfg := config.Config{
		Host:      srv.URL,
		Timeout:   2 * time.Second,
		CacheDir:  dir,
		ModelsTTL: time.Second,
		JSONOutput: true,
	}
	code := runModels(context.Background(), cfg, &stdout, &stderr)
	if code != 22 {
		t.Fatalf("exit = %d, want 22; stderr=%s", code, stderr.String())
	}
	env := decodeEnvelope(t, stdout.String())
	if env.Command != "models" {
		t.Errorf("command = %q, want models", env.Command)
	}
	if env.Ok {
		t.Errorf("ok = true, want false")
	}
	if env.Error == nil {
		t.Fatal("error is nil, want non-nil")
	}
	if env.Error.Code != "http_error" {
		t.Errorf("error.code = %q, want http_error", env.Error.Code)
	}
}

// ── msg ──────────────────────────────────────────────────────────────────────

func TestRunMsg_JSON_Happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(chatCanned))
	}))
	defer srv.Close()

	defer stubTokenSource(t)()

	var stdout, stderr bytes.Buffer
	cfg := config.Config{Host: srv.URL, Timeout: 2 * time.Second, JSONOutput: true}
	code := runMsg(context.Background(), cfg, []string{"default", "hi"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	env := decodeEnvelope(t, stdout.String())
	if env.Command != "msg" {
		t.Errorf("command = %q, want msg", env.Command)
	}
	if !env.Ok {
		t.Errorf("ok = false, want true")
	}
	if env.Error != nil {
		t.Errorf("error = %v, want nil", env.Error)
	}
	// data should have the ToolResponse core fields
	var data map[string]any
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("data is not JSON: %v", err)
	}
	if data["agent"] != "openclaw/default" {
		t.Errorf("data.agent = %v, want openclaw/default", data["agent"])
	}
	if data["content"] != "hello world" {
		t.Errorf("data.content = %v, want hello world", data["content"])
	}
	if data["finish_reason"] != "stop" {
		t.Errorf("data.finish_reason = %v, want stop", data["finish_reason"])
	}
	if _, ok := data["usage"]; !ok {
		t.Error("data.usage missing")
	}
	if _, ok := data["redactions"]; !ok {
		t.Error("data.redactions missing")
	}
}

func TestRunMsg_JSON_Error(t *testing.T) {
	// Closed server → connection refused → exit 7.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close()

	defer stubTokenSource(t)()

	var stdout, stderr bytes.Buffer
	cfg := config.Config{Host: addr, Timeout: 1 * time.Second, JSONOutput: true}
	code := runMsg(context.Background(), cfg, []string{"default", "hi"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 7 {
		t.Fatalf("exit = %d, want 7; stderr=%s", code, stderr.String())
	}
	env := decodeEnvelope(t, stdout.String())
	if env.Command != "msg" {
		t.Errorf("command = %q, want msg", env.Command)
	}
	if env.Ok {
		t.Errorf("ok = true, want false")
	}
	if env.Error == nil {
		t.Fatal("error is nil, want non-nil")
	}
	if env.Error.Code != "conn_refused" {
		t.Errorf("error.code = %q, want conn_refused", env.Error.Code)
	}
	// trace_id should be set (msg generates one before the API call)
	if env.Error.TraceID == "" {
		t.Error("error.trace_id is empty, want a trace-id")
	}
}

// ── verify ───────────────────────────────────────────────────────────────────

func TestRunVerify_JSON_Happy(t *testing.T) {
	installFakeGitGH(t)
	t.Setenv("GIT_CATFILE_T_OUT", "commit\n")
	t.Setenv("GIT_CATFILE_T_EXIT", "0")

	var stdout, stderr bytes.Buffer
	cfg := config.Config{JSONOutput: true}
	code := runVerify(context.Background(), cfg, []string{"commit", "deadbeef"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	env := decodeEnvelope(t, stdout.String())
	if env.Command != "verify" {
		t.Errorf("command = %q, want verify", env.Command)
	}
	if !env.Ok {
		t.Errorf("ok = false, want true")
	}
	if env.Error != nil {
		t.Errorf("error = %v, want nil", env.Error)
	}
	var data map[string]any
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("data is not JSON: %v", err)
	}
	msg, _ := data["message"].(string)
	if !strings.Contains(msg, "verified") {
		t.Errorf("data.message = %q, want to contain 'verified'", msg)
	}
}

func TestRunVerify_JSON_Error(t *testing.T) {
	installFakeGitGH(t)
	t.Setenv("GIT_CATFILE_T_EXIT", "128")
	t.Setenv("GIT_REVPARSE_OUT", "/some/repo\n")

	var stdout, stderr bytes.Buffer
	cfg := config.Config{JSONOutput: true}
	code := runVerify(context.Background(), cfg, []string{"commit", "nope"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1; stderr=%s", code, stderr.String())
	}
	env := decodeEnvelope(t, stdout.String())
	if env.Command != "verify" {
		t.Errorf("command = %q, want verify", env.Command)
	}
	if env.Ok {
		t.Errorf("ok = true, want false")
	}
	if env.Error == nil {
		t.Fatal("error is nil, want non-nil")
	}
	if env.Error.Code != "not_found" {
		t.Errorf("error.code = %q, want not_found", env.Error.Code)
	}
	if !strings.Contains(env.Error.Message, "unverified") {
		t.Errorf("error.message = %q, want to contain 'unverified'", env.Error.Message)
	}
}

// ── trace ────────────────────────────────────────────────────────────────────

func TestRunTrace_JSON_Happy(t *testing.T) {
	const traceBody = `{"data":[{"spans":[{"operationName":"op","duration":1000,"processID":"p1"}],"processes":{"p1":{"serviceName":"svc"}}}],"errors":[]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(traceBody))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	cfg := config.Config{JaegerUI: srv.URL, JSONOutput: true}
	code := runTrace(context.Background(), cfg, []string{"abc123"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	env := decodeEnvelope(t, stdout.String())
	if env.Command != "trace" {
		t.Errorf("command = %q, want trace", env.Command)
	}
	if !env.Ok {
		t.Errorf("ok = false, want true")
	}
	if env.Error != nil {
		t.Errorf("error = %v, want nil", env.Error)
	}
	var data traceJSONData
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("data unmarshal: %v", err)
	}
	if data.TraceID != "abc123" {
		t.Errorf("data.trace_id = %q, want abc123", data.TraceID)
	}
	if !strings.Contains(data.UIURL, "abc123") {
		t.Errorf("data.ui_url = %q, want to contain abc123", data.UIURL)
	}
	if data.SpansCount == nil || *data.SpansCount != 1 {
		t.Errorf("data.spans_count = %v, want 1", data.SpansCount)
	}
}

func TestRunTrace_JSON_MissingJaegerUI(t *testing.T) {
	var stdout, stderr bytes.Buffer
	cfg := config.Config{JaegerUI: "", JSONOutput: true}
	code := runTrace(context.Background(), cfg, []string{"abc123"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	env := decodeEnvelope(t, stdout.String())
	if env.Command != "trace" {
		t.Errorf("command = %q, want trace", env.Command)
	}
	if env.Ok {
		t.Errorf("ok = true, want false")
	}
	if env.Error == nil {
		t.Fatal("error is nil, want non-nil")
	}
	if env.Error.Code != "usage_error" {
		t.Errorf("error.code = %q, want usage_error", env.Error.Code)
	}
}

// ── helpers for existing tests that need a temp cache dir ───────────────────

func stubModelsCache(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	// pre-populate cache so models doesn't need a live server
	body := []byte(`{"object":"list","data":[]}`)
	_ = os.WriteFile(filepath.Join(dir, "models.json"), body, 0o644)
	return dir
}
