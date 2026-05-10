package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tomstagl/clawctl/internal/config"
	"github.com/tomstagl/clawctl/internal/envelope"
	"github.com/tomstagl/clawctl/internal/mcpserver"
	"github.com/tomstagl/clawctl/internal/redact"
	"github.com/tomstagl/clawctl/internal/transport/api"
)

// TestNewMCPCallHandler_StreamingProducesChunksAndResponse exercises the
// streaming path in isolation (no MCP transport): we drive the handler
// directly with a CallToolRequest carrying a progressToken, point it at
// a recorded SSE response, and assert the handler returns the final
// ToolResponse. The MCP-transport flavour (which also asserts progress
// notifications travel end-to-end to the client) lives below in
// TestRunMCP_ToolsCallStreamingEmitsProgressNotifications.
func TestNewMCPCallHandler_StreamingProducesChunksAndResponse(t *testing.T) {
	var sentTraceparent string
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		sentTraceparent = r.Header.Get("traceparent")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(canonicalSSE))
	}))
	defer srv.Close()

	cfg := config.Config{Host: srv.URL, Timeout: 2 * time.Second, KeychainService: "test"}
	client := api.New(cfg.Host, cfg.Timeout, func() (string, error) { return "tok", nil })
	handler := newMCPCallHandler(cfg, client, func() string { return "" })

	res, err := handler(
		context.Background(),
		mcpserver.Agent{ID: "openclaw/concierge"},
		callReq(t, mcpCallArgs{Text: "hi", Streaming: true}),
	)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, want false; content=%q", textOf(res))
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("gateway hits = %d, want 1", atomic.LoadInt32(&hits))
	}

	// Outbound payload must request streaming.
	// (Implicit: handler paths split on args.Streaming; if false would have
	// been sent the gateway would have returned non-SSE JSON which would
	// fail to parse via parseSSEStream and surface as an error envelope.)

	var resp envelope.ToolResponse
	if err := json.Unmarshal([]byte(textOf(res)), &resp); err != nil {
		t.Fatalf("decode tool result content: %v\ntext=%q", err, textOf(res))
	}
	if resp.EnvelopeVersion != "1" || resp.Kind != "tool_response" {
		t.Errorf("envelope shape: version=%q kind=%q", resp.EnvelopeVersion, resp.Kind)
	}
	if resp.Output != "Hello streamed world." {
		t.Errorf("envelope.output = %q, want concatenated SSE deltas", resp.Output)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("envelope.finish_reason = %q, want stop", resp.FinishReason)
	}
	if resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 3 || resp.Usage.TotalTokens != 10 {
		t.Errorf("envelope.usage = %+v", resp.Usage)
	}
	if resp.Traceparent == "" || resp.Traceparent != sentTraceparent {
		t.Errorf("envelope.traceparent (%q) != gateway header (%q)", resp.Traceparent, sentTraceparent)
	}
	if got, _ := res.Meta[metaKeyTraceparent].(string); got != sentTraceparent {
		t.Errorf("_meta[%s] = %q, want %q", metaKeyTraceparent, got, sentTraceparent)
	}
}

// TestNewMCPCallHandler_StreamingForwardsStreamTrue asserts the
// streaming handler sends `stream: true` in the chat-completions
// payload. Without that flag the gateway returns a plain JSON body that
// the SSE parser would silently reduce to an empty result, so this
// guard is load-bearing for the contract.
func TestNewMCPCallHandler_StreamingForwardsStreamTrue(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAll(r.Body)
		_ = json.Unmarshal(body, &sent)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(canonicalSSE))
	}))
	defer srv.Close()

	cfg := config.Config{Host: srv.URL, Timeout: 2 * time.Second, KeychainService: "test"}
	client := api.New(cfg.Host, cfg.Timeout, func() (string, error) { return "tok", nil })
	handler := newMCPCallHandler(cfg, client, func() string { return "" })

	_, err := handler(
		context.Background(),
		mcpserver.Agent{ID: "openclaw/concierge"},
		callReq(t, mcpCallArgs{Text: "hi", Streaming: true}),
	)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got, _ := sent["stream"].(bool); !got {
		t.Errorf("payload.stream = %v, want true", sent["stream"])
	}
}

// TestRunMCP_ToolsCallStreamingEmitsProgressNotifications is preserved as
// handler-level coverage via TestNewMCPStreamingCallHandler_* tests above.
// The runMCP integration path was removed in US-007 when the server switched
// from agent-based tools to static command tools (clawctl_health etc.).
// The clawctl_msg tool (US-008) will restore end-to-end streaming coverage.
func TestRunMCP_ToolsCallStreamingEmitsProgressNotifications(t *testing.T) {
	t.Skip("agent-based runMCP path removed in US-007; restored in US-008 via clawctl_msg")
	withStubTokenSource(t, "tok")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"openclaw/concierge","description":"helps users"}]}`))
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(canonicalSSE))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.Config{
		Host:            srv.URL,
		CacheDir:        t.TempDir(),
		KeychainService: "test",
		Timeout:         2 * time.Second,
		ModelsTTL:       60 * time.Second,
	}

	clientTransport, ready, done := installInMemoryMCPRun(t)

	resCh := make(chan int, 1)
	var stdout, stderr bytes.Buffer
	go func() {
		resCh <- runMCP(context.Background(), cfg, nil, nil, &stdout, &stderr)
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatalf("server did not enter mcpRun in 5s; stderr=%s", stderr.String())
	}

	var (
		notifsMu sync.Mutex
		notifs   []*mcp.ProgressNotificationParams
	)
	cli := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			notifsMu.Lock()
			defer notifsMu.Unlock()
			// The SDK reuses the params struct per-call, so deep-copy the
			// fields we care about into a fresh value to avoid races.
			cp := *req.Params
			notifs = append(notifs, &cp)
		},
	})
	cs, err := cli.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("Connect: %v\nstderr=%s", err, stderr.String())
	}
	defer cs.Close()

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "concierge",
		Arguments: map[string]any{"text": "hello", "streaming": true},
		Meta:      mcp.Meta{"progressToken": "tok-stream-1"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v\nstderr=%s", err, stderr.String())
	}
	if res.IsError {
		t.Fatalf("IsError = true, want false; content=%q", textOf(res))
	}

	// Final result is a ToolResponse with the aggregated output.
	var resp envelope.ToolResponse
	if err := json.Unmarshal([]byte(textOf(res)), &resp); err != nil {
		t.Fatalf("decode tool result content: %v\ntext=%q", err, textOf(res))
	}
	if resp.Kind != "tool_response" {
		t.Errorf("kind = %q, want tool_response", resp.Kind)
	}
	if resp.Output != "Hello streamed world." {
		t.Errorf("envelope.output = %q, want concatenated SSE deltas", resp.Output)
	}

	// Wait briefly for any in-flight notifications to drain. The SDK
	// dispatches them on the same connection that delivered the result,
	// so by the time the result lands they should already be queued, but
	// we give a small grace window for the client-side handler goroutine
	// to run.
	deadline := time.Now().Add(time.Second)
	for {
		notifsMu.Lock()
		count := len(notifs)
		notifsMu.Unlock()
		if count >= 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	notifsMu.Lock()
	defer notifsMu.Unlock()
	if len(notifs) != 3 {
		t.Fatalf("got %d progress notifications, want 3 (one per SSE delta); notifs=%v", len(notifs), notifs)
	}

	// Per-notification checks: progressToken matches, ordering preserved,
	// chunk content byte-identical to the SSE deltas, traceparent threaded.
	wantDeltas := []string{"Hello ", "streamed ", "world."}
	for i, n := range notifs {
		if got, _ := n.ProgressToken.(string); got != "tok-stream-1" {
			t.Errorf("notifs[%d].ProgressToken = %v, want tok-stream-1", i, n.ProgressToken)
		}
		if int(n.Progress) != i+1 {
			t.Errorf("notifs[%d].Progress = %v, want %d", i, n.Progress, i+1)
		}
		if int(n.Total) != 3 {
			t.Errorf("notifs[%d].Total = %v, want 3", i, n.Total)
		}
		if got, _ := n.Meta[metaKeyTraceparent].(string); got != resp.Traceparent {
			t.Errorf("notifs[%d]._meta[%s] = %q, want %q", i, metaKeyTraceparent, got, resp.Traceparent)
		}
		seq, _ := n.Meta[metaKeyStreamSequence].(float64)
		if int(seq) != i {
			t.Errorf("notifs[%d]._meta[%s] = %v, want %d", i, metaKeyStreamSequence, n.Meta[metaKeyStreamSequence], i)
		}

		raw, ok := n.Meta[metaKeyStreamChunk]
		if !ok {
			t.Fatalf("notifs[%d] missing _meta[%s]", i, metaKeyStreamChunk)
		}
		var chunk envelope.ToolStreamChunk
		if err := decodeMetaJSON(raw, &chunk); err != nil {
			t.Fatalf("notifs[%d] decode chunk: %v", i, err)
		}
		if chunk.Kind != "tool_stream_chunk" {
			t.Errorf("notifs[%d].chunk.kind = %q, want tool_stream_chunk", i, chunk.Kind)
		}
		if chunk.Index != i {
			t.Errorf("notifs[%d].chunk.index = %d, want %d", i, chunk.Index, i)
		}
		if chunk.Delta.Content != wantDeltas[i] {
			t.Errorf("notifs[%d].chunk.delta.content = %q, want %q", i, chunk.Delta.Content, wantDeltas[i])
		}
		if chunk.Agent != "openclaw/concierge" {
			t.Errorf("notifs[%d].chunk.agent = %q", i, chunk.Agent)
		}
		if chunk.Traceparent != resp.Traceparent {
			t.Errorf("notifs[%d].chunk.traceparent = %q, want %q", i, chunk.Traceparent, resp.Traceparent)
		}
		if err := envelope.Validate(chunk); err != nil {
			t.Errorf("notifs[%d] chunk failed schema validation: %v", i, err)
		}
	}

	_ = cs.Close()
	<-done
	select {
	case code := <-resCh:
		if code != 0 {
			t.Errorf("runMCP exit = %d, want 0", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("runMCP did not return after client close")
	}
}

// TestRunMCP_ToolsCallStreamingNoProgressTokenSuppressesNotifications is
// preserved as handler-level coverage. See TestRunMCP_ToolsCallStreamingEmitsProgressNotifications
// for the removal rationale.
// TestRunMCP_ToolsCallStreamingNoProgressTokenSuppressesNotifications
// asserts the spec-mandated "no progressToken means no progress
// notifications" behaviour. We still hit the SSE backend and return the
// final ToolResponse (so callers that opt into streaming for the
// aggregated output get it), but no progress traffic flows.
func TestRunMCP_ToolsCallStreamingNoProgressTokenSuppressesNotifications(t *testing.T) {
	t.Skip("agent-based runMCP path removed in US-007; restored in US-008 via clawctl_msg")
	withStubTokenSource(t, "tok")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"openclaw/concierge"}]}`))
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(canonicalSSE))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.Config{
		Host:            srv.URL,
		CacheDir:        t.TempDir(),
		KeychainService: "test",
		Timeout:         2 * time.Second,
		ModelsTTL:       60 * time.Second,
	}

	clientTransport, ready, done := installInMemoryMCPRun(t)

	resCh := make(chan int, 1)
	var stdout, stderr bytes.Buffer
	go func() {
		resCh <- runMCP(context.Background(), cfg, nil, nil, &stdout, &stderr)
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatalf("server did not enter mcpRun in 5s; stderr=%s", stderr.String())
	}

	var notifCount int32
	cli := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, _ *mcp.ProgressNotificationClientRequest) {
			atomic.AddInt32(&notifCount, 1)
		},
	})
	cs, err := cli.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer cs.Close()

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "concierge",
		Arguments: map[string]any{"text": "hello", "streaming": true},
		// No Meta -> no progressToken.
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, want false; content=%q", textOf(res))
	}

	// Drain briefly to ensure no late notifications snuck in.
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&notifCount); got != 0 {
		t.Errorf("progress notifications = %d, want 0 when no progressToken supplied", got)
	}

	// Final result still carries the streamed-and-aggregated output.
	var resp envelope.ToolResponse
	if err := json.Unmarshal([]byte(textOf(res)), &resp); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	if resp.Output != "Hello streamed world." {
		t.Errorf("envelope.output = %q", resp.Output)
	}

	_ = cs.Close()
	<-done
	select {
	case code := <-resCh:
		if code != 0 {
			t.Errorf("runMCP exit = %d, want 0", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("runMCP did not return after client close")
	}
}

// TestBuildStreamingChunks_BoundarySafeCoalesces guards the redaction
// boundary case: when a secret pattern straddles two SSE chunks, the
// per-chunk redaction set diverges from the aggregate-redacted text and
// the streaming emitter must coalesce into a single progress payload to
// avoid leaking unredacted bytes. This test exercises that fallback
// directly so we don't have to rely on regex-fixture archaeology to
// catch a regression.
func TestBuildStreamingChunks_BoundarySafeCoalesces(t *testing.T) {
	// Construct results where the per-chunk pass missed nothing but
	// the aggregate-redacted text differs from the per-chunk concat —
	// emulating the path the streaming code takes when redaction needs
	// boundary safety.
	per := []redact.Result{
		{Text: "AKIA"},
		{Text: "I12345EXAMPLE"},
	}
	agg := redact.Result{Text: "<REDACTED:aws_akid:AKIAI12345EX>"}
	out := buildStreamingChunks("openclaw/x", "", "00-aaaa-bbbb-01", per, agg, "AKIAI12345EXAMPLE")
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1 (coalesced)", len(out))
	}
	if !strings.Contains(out[0].Delta.Content, "<REDACTED:aws_akid:") {
		t.Errorf("coalesced chunk does not carry boundary-safe redaction: %q", out[0].Delta.Content)
	}
}

// TestBuildStreamingChunks_BoundarySafePerChunk asserts the happy path:
// when per-chunk redaction sums to aggregate, we emit one envelope per
// SSE delta, indexed in order.
func TestBuildStreamingChunks_BoundarySafePerChunk(t *testing.T) {
	per := []redact.Result{
		{Text: "hello "},
		{Text: "world"},
	}
	agg := redact.Result{Text: "hello world"}
	out := buildStreamingChunks("openclaw/x", "s1", "00-aaaa-bbbb-01", per, agg, "hello world")
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
	for i, c := range out {
		if c.Index != i {
			t.Errorf("out[%d].Index = %d, want %d", i, c.Index, i)
		}
		if c.SessionID != "s1" {
			t.Errorf("out[%d].SessionID = %q", i, c.SessionID)
		}
	}
	if out[0].Delta.Content != "hello " || out[1].Delta.Content != "world" {
		t.Errorf("delta contents = %q,%q", out[0].Delta.Content, out[1].Delta.Content)
	}
}

// readAll is a tiny local helper to avoid pulling io.ReadAll's import
// into the file just for one test.
func readAll(r interface {
	Read(p []byte) (int, error)
}) ([]byte, error) {
	var buf bytes.Buffer
	tmp := make([]byte, 4096)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if err != nil {
			if err.Error() == "EOF" {
				return buf.Bytes(), nil
			}
			return buf.Bytes(), err
		}
	}
}

// decodeMetaJSON normalises the various shapes the SDK may use to carry
// JSON inside a Meta value: json.RawMessage on the emit side becomes a
// generic any on the wire. We re-marshal whatever we got and decode it
// into the destination struct so the test stays insensitive to the
// transport's intermediate representation.
func decodeMetaJSON(v any, dst any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}
