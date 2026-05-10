// Package sse parses Server-Sent Events as defined by the HTML Living
// Standard (https://html.spec.whatwg.org/multipage/server-sent-events.html).
//
// The parser is intentionally protocol-pure: it yields raw SSE events with
// their type, id, and concatenated data fields. It knows nothing about
// payload-layer conventions such as the OpenAI-style "data: [DONE]" stream
// terminator — those belong to the caller. Splitting the layers lets the
// streaming subcommands evolve their interpretation of payloads without
// destabilising the byte-level wire parser.
//
// The parser handles three classes of awkward input that production SSE
// readers must cope with:
//
//   - bytes split across event boundaries (network frame fragmentation), via
//     bufio buffering of the underlying io.Reader;
//   - bytes split mid-UTF-8 codepoint, by treating the data field as opaque
//     bytes and never decoding runes during parsing;
//   - streams that close without a final blank line (a stand-in for the
//     "missing terminal [DONE]" condition in OpenAI streams), by flushing
//     any pending event when the underlying reader returns io.EOF.
//
// Strict spec conformance would discard a pending event on EOF; we flush
// instead, because clawctl's debugging story benefits from surfacing the
// last partial event the gateway managed to send.
package sse

import (
	"bufio"
	"io"
	"strings"
)

// Event is a parsed Server-Sent Event.
type Event struct {
	// ID is the most recently seen "id:" field. Carries forward across
	// events per the SSE spec; cleared only when an explicit empty id is
	// received. Empty if no id has ever appeared in the stream.
	ID string
	// Type is the "event:" field, defaulting to "message" when not set.
	Type string
	// Data is the concatenation of "data:" fields joined by '\n'.
	Data string
}

// Parser yields events from an io.Reader carrying an SSE stream.
//
// A single Parser is not safe for concurrent use; create one per stream.
type Parser struct {
	br      *bufio.Reader
	curID   string
	pending pendingEvent
	done    bool
}

type pendingEvent struct {
	// used is set whenever a dispatch-eligible field (event or data) has
	// been seen. id and retry update parser state without arming dispatch,
	// matching the SSE spec's separation between "buffers" and "last
	// event ID".
	used      bool
	eventType string
	dataBuf   strings.Builder
	hadData   bool
}

// New constructs a Parser over r.
func New(r io.Reader) *Parser {
	return &Parser{br: bufio.NewReaderSize(r, 4096)}
}

// Next returns the next event, or io.EOF when the stream ends. Errors other
// than io.EOF surface read failures from the underlying reader; callers
// should treat them as terminal.
//
// If the stream closes mid-event (no final blank line), Next flushes the
// partially-built event with whatever fields it accumulated, then returns
// io.EOF on the following call.
func (p *Parser) Next() (Event, error) {
	for {
		if p.done {
			if p.pending.used {
				return p.flush(), nil
			}
			return Event{}, io.EOF
		}
		line, err := p.readLine()
		if err == io.EOF {
			p.done = true
			if line == "" {
				continue
			}
		} else if err != nil {
			return Event{}, err
		}
		if line == "" {
			if p.pending.used {
				return p.flush(), nil
			}
			p.pending = pendingEvent{}
			continue
		}
		if strings.HasPrefix(line, ":") {
			// Comment line: ignored per spec.
			continue
		}
		field, value := splitField(line)
		switch field {
		case "event":
			p.pending.used = true
			p.pending.eventType = value
		case "data":
			p.pending.used = true
			if p.pending.hadData {
				p.pending.dataBuf.WriteByte('\n')
			}
			p.pending.dataBuf.WriteString(value)
			p.pending.hadData = true
		case "id":
			// SSE spec: NUL bytes invalidate the field; the last-event-id
			// is otherwise updated immediately, regardless of whether the
			// surrounding event eventually dispatches.
			if !strings.Contains(value, "\x00") {
				p.curID = value
			}
		case "retry":
			// Ignored: clawctl does not implement automatic reconnection.
		}
	}
}

func (p *Parser) flush() Event {
	ev := Event{
		ID:   p.curID,
		Type: p.pending.eventType,
		Data: p.pending.dataBuf.String(),
	}
	if ev.Type == "" {
		ev.Type = "message"
	}
	p.pending = pendingEvent{}
	return ev
}

// readLine consumes one line, handling \n, \r\n, and bare \r terminators
// (all three are valid per the SSE spec). The terminator is stripped from
// the returned string. On io.EOF the trailing fragment, if any, is returned
// alongside the error.
func (p *Parser) readLine() (string, error) {
	var buf []byte
	for {
		b, err := p.br.ReadByte()
		if err != nil {
			return string(buf), err
		}
		if b == '\n' {
			return string(buf), nil
		}
		if b == '\r' {
			next, err := p.br.ReadByte()
			if err == nil && next != '\n' {
				_ = p.br.UnreadByte()
			}
			return string(buf), nil
		}
		buf = append(buf, b)
	}
}

// splitField parses a "field:value" line per the SSE spec: the field is the
// prefix before the first colon; the value is the suffix, with at most one
// leading space stripped. Lines without a colon are entirely the field name
// with an empty value.
func splitField(line string) (string, string) {
	idx := strings.IndexByte(line, ':')
	if idx < 0 {
		return line, ""
	}
	field := line[:idx]
	value := line[idx+1:]
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	return field, value
}
