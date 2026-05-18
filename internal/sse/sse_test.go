package sse_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/tomstagl/clawctl/internal/sse"
)

// drain reads every event the parser emits, returning the slice and the
// terminal error (always io.EOF when the input is well-formed).
func drain(t *testing.T, p *sse.Parser) []sse.Event {
	t.Helper()
	var out []sse.Event
	for {
		ev, err := p.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		out = append(out, ev)
	}
}

func TestSSEParseSimple(t *testing.T) {
	in := "data: hello\n\ndata: world\n\n"
	got := drain(t, sse.New(strings.NewReader(in)))
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2: %#v", len(got), got)
	}
	if got[0].Data != "hello" || got[0].Type != "message" {
		t.Errorf("event 0: %#v", got[0])
	}
	if got[1].Data != "world" {
		t.Errorf("event 1: %#v", got[1])
	}
}

func TestSSEParseMultilineData(t *testing.T) {
	// Multiple data: fields concatenate with \n per the SSE spec.
	in := "data: line1\ndata: line2\ndata: line3\n\n"
	got := drain(t, sse.New(strings.NewReader(in)))
	if len(got) != 1 || got[0].Data != "line1\nline2\nline3" {
		t.Fatalf("got %#v, want single event with three lines joined", got)
	}
}

func TestSSEParseSplitMidEvent(t *testing.T) {
	// iotest.OneByteReader returns one byte per Read call. This stresses
	// every byte boundary in the input — every newline, every field char,
	// every UTF-8 continuation byte. If the parser leaks a buffer state
	// or relies on whole-line reads from the underlying reader, this
	// test will catch it.
	in := "event: greet\ndata: hello world\n\nevent: bye\ndata: see ya\n\n"
	r := iotest.OneByteReader(strings.NewReader(in))
	got := drain(t, sse.New(r))
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	if got[0].Type != "greet" || got[0].Data != "hello world" {
		t.Errorf("event 0: %#v", got[0])
	}
	if got[1].Type != "bye" || got[1].Data != "see ya" {
		t.Errorf("event 1: %#v", got[1])
	}
}

func TestSSEParseSplitMidUTF8Codepoint(t *testing.T) {
	// Place a multi-byte codepoint (U+1F600 GRINNING FACE, 4 bytes
	// 0xF0 0x9F 0x98 0x80) inside a data field, then read one byte at
	// a time. The parser must preserve the bytes verbatim — Go strings
	// are byte sequences, so as long as we don't try to decode runes
	// during framing, the codepoint reassembles intact.
	const grinning = "\xF0\x9F\x98\x80"
	in := "data: pre" + grinning + "post\n\n"
	r := iotest.OneByteReader(strings.NewReader(in))
	got := drain(t, sse.New(r))
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].Data != "pre"+grinning+"post" {
		t.Errorf("data not preserved: % x", got[0].Data)
	}
}

func TestSSEParseMissingTerminalDone(t *testing.T) {
	// Stream that never sends "data: [DONE]" — i.e. the OpenAI app-level
	// terminator is absent. The parser does not care about [DONE]; it
	// yields all properly-delimited events and then returns io.EOF on
	// the next call. The final event has its blank-line termination, so
	// nothing is dropped.
	in := "data: chunk1\n\ndata: chunk2\n\n"
	got := drain(t, sse.New(strings.NewReader(in)))
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	if got[0].Data != "chunk1" || got[1].Data != "chunk2" {
		t.Errorf("data lost: %#v", got)
	}
	for _, e := range got {
		if e.Data == "[DONE]" {
			t.Errorf("[DONE] should not appear in a stream that omitted it")
		}
	}
}

func TestSSEParseStreamClosedMidEventFlushes(t *testing.T) {
	// A trailing event without its blank-line terminator is flushed at
	// EOF. Strict spec conformance would discard it; we keep it for
	// debuggability — see the package doc comment.
	in := "data: complete\n\ndata: partial"
	got := drain(t, sse.New(strings.NewReader(in)))
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	if got[1].Data != "partial" {
		t.Errorf("partial event not flushed at EOF: %#v", got[1])
	}
}

func TestSSEParseEventErrorLine(t *testing.T) {
	// "event: error" is how the gateway signals a stream-mid failure.
	// The parser surfaces it as Event.Type so the caller can branch.
	in := "event: error\ndata: rate_limited\n\ndata: should-not-appear\n\n"
	got := drain(t, sse.New(strings.NewReader(in)))
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	if got[0].Type != "error" {
		t.Errorf("event 0 type: %q, want %q", got[0].Type, "error")
	}
	if got[0].Data != "rate_limited" {
		t.Errorf("event 0 data: %q", got[0].Data)
	}
	if got[1].Type != "message" {
		t.Errorf("event 1 type: %q, want default 'message'", got[1].Type)
	}
}

func TestSSEParseCRLFAndCR(t *testing.T) {
	// Per spec, all three line terminators (\n, \r\n, bare \r) are
	// equivalent. Mix them in one stream and confirm framing.
	in := "data: a\r\ndata: b\r\rdata: c\n\n"
	got := drain(t, sse.New(strings.NewReader(in)))
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2: %#v", len(got), got)
	}
	if got[0].Data != "a\nb" {
		t.Errorf("event 0 data: %q, want %q", got[0].Data, "a\nb")
	}
	if got[1].Data != "c" {
		t.Errorf("event 1 data: %q, want %q", got[1].Data, "c")
	}
}

func TestSSEParseCommentLinesIgnored(t *testing.T) {
	// Lines starting with ':' are comments per spec — ignored entirely.
	// Used in practice for keep-alive heartbeats; surfacing them as
	// events would clutter the caller's stream.
	in := ": keep-alive\n\n: another\ndata: real\n\n"
	got := drain(t, sse.New(strings.NewReader(in)))
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1: %#v", len(got), got)
	}
	if got[0].Data != "real" {
		t.Errorf("data: %q", got[0].Data)
	}
}

func TestSSEParseIDPersistence(t *testing.T) {
	// Per spec, the last-event-id carries forward across events until
	// explicitly changed. NUL bytes invalidate the field.
	in := "id: 1\ndata: a\n\ndata: b\n\nid: 2\ndata: c\n\n"
	got := drain(t, sse.New(strings.NewReader(in)))
	if len(got) != 3 {
		t.Fatalf("got %d events", len(got))
	}
	if got[0].ID != "1" || got[1].ID != "1" || got[2].ID != "2" {
		t.Errorf("ids: %q %q %q", got[0].ID, got[1].ID, got[2].ID)
	}
}

func TestSSEParseLeadingSpaceStripped(t *testing.T) {
	// Per spec, exactly one leading space after the colon is stripped.
	// Two spaces should leave one in the value.
	in := "data:  two-spaces\n\ndata:no-space\n\n"
	got := drain(t, sse.New(strings.NewReader(in)))
	if len(got) != 2 {
		t.Fatalf("got %d events", len(got))
	}
	if got[0].Data != " two-spaces" {
		t.Errorf("event 0 data: %q, want %q", got[0].Data, " two-spaces")
	}
	if got[1].Data != "no-space" {
		t.Errorf("event 1 data: %q", got[1].Data)
	}
}

func TestSSEParseOpenAIShape(t *testing.T) {
	// Smoke test against the shape of an OpenAI streaming response so
	// the package isn't validated only on synthetic inputs. The parser
	// itself doesn't interpret the JSON payload — it just frames events
	// — but the framing has to survive realistic chunks.
	in := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"hel"}}]}`,
		``,
		`data: {"choices":[{"delta":{"content":"lo"}}]}`,
		``,
		`data: [DONE]`,
		``,
		``,
	}, "\n")
	got := drain(t, sse.New(strings.NewReader(in)))
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	if got[2].Data != "[DONE]" {
		t.Errorf("expected [DONE] sentinel, got %q", got[2].Data)
	}
}

// FuzzParseSSE asserts the parser never panics on arbitrary inputs and
// always reaches io.EOF for finite ones. The seed corpus covers the four
// edge cases the package promises to handle:
//
//  1. split-mid-event (via mid-line, mid-field bytes);
//  2. split-mid-UTF-8 codepoint (a partial U+1F600);
//  3. missing terminal [DONE] (a stream that ends without the OpenAI sentinel);
//  4. event:error lines.
//
// CI runs this with `go test -run=^$ -fuzz=FuzzParseSSE -fuzztime=5s` —
// the 5s budget is enough to exercise the seeds plus mutation neighbours
// without slowing the build noticeably.
func FuzzParseSSE(f *testing.F) {
	f.Add([]byte("data: hello\n\n"))
	f.Add([]byte("event: greet\ndata: hi\n\nevent: bye"))                                  // split-mid-event, no terminator
	f.Add([]byte("data: pre\xF0\x9F\x98\x80post\n\n"))                                    // valid 4-byte codepoint
	f.Add([]byte("data: prefix\xF0\x9F"))                                                  // partial UTF-8 at EOF
	f.Add([]byte("data: chunk1\n\ndata: chunk2\n\n"))                                      // missing [DONE]
	f.Add([]byte("event: error\ndata: rate_limited\n\n"))                                  // event:error
	f.Add([]byte(": keep-alive\nid: 1\nretry: 5000\nevent: foo\ndata: bar\ndata: baz\n\n")) // every field type
	f.Add([]byte("data: a\r\ndata: b\rdata: c\n\n"))                                       // mixed line endings

	f.Fuzz(func(t *testing.T, in []byte) {
		// Parser must not panic, must terminate, and must not read past
		// the end of the input. Wrapping the input in a bytes.Reader
		// gives us a deterministic io.Reader; an event count cap guards
		// against an infinite loop bug regression.
		p := sse.New(bytes.NewReader(in))
		const maxEvents = 1 << 14
		count := 0
		for {
			_, err := p.Next()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				return
			}
			count++
			if count > maxEvents {
				t.Fatalf("parser produced > %d events from %d-byte input — likely infinite loop", maxEvents, len(in))
			}
		}
	})
}
