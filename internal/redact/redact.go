// Package redact masks well-known secret patterns in clawctl response
// text. It mirrors the perl-regex set in the bash entrypoint's _redact
// helper byte-for-byte so the typed Go binary and the bash MVP produce
// identical redacted output, identical sink JSON, and identical audit-
// file lines for the same input. test/parity-redact.sh enforces this.
//
// Patterns are iterated alphabetically by kind to match bash's
// `sort keys %pat` substitution order. Hits carry an offset_hint into
// the *pre-redaction* input so the order is stable regardless of
// replacement length, and so envelope emitters can populate
// envelope.redactions[] without re-parsing stderr (US-008 contract).
package redact

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Hit records one secret-pattern match in the input. OffsetHint is the
// byte offset of the match in the original (pre-redaction) text.
type Hit struct {
	Kind       string `json:"kind"`
	OffsetHint int    `json:"offset_hint"`
	Count      int    `json:"count"`
}

// Result bundles the redacted text with the per-match hit list, sorted
// by (offset_hint, kind) for byte-stable sink output.
type Result struct {
	Text string
	Hits []Hit
}

// Kinds returns the sorted-unique kind names from r.Hits, suitable for
// the stderr WARNING line and audit-file entry.
func (r Result) Kinds() []string {
	seen := map[string]struct{}{}
	for _, h := range r.Hits {
		seen[h.Kind] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Options tunes the redactor.
//
//   - GwToken adds a literal-substring redaction kind "gw_token" when
//     non-empty AND length >= 16 (matches bash's length bound; without
//     it, a short or empty token would redact every short word).
//   - Disable returns the input unchanged with an empty Hits list.
//     Honors CLAWCTL_NO_REDACT=1 at the call site.
type Options struct {
	GwToken string
	Disable bool
}

// patternKinds is the ordered (alphabetical) list of bash-parity
// patterns. Order is load-bearing: substituting in a different order
// would still yield correct redactions in practice (the patterns don't
// overlap), but locking the order keeps the diff against bash trivial
// when a pattern set changes in future.
var patternKinds = []struct {
	Kind    string
	Pattern *regexp.Regexp
}{
	{Kind: "aws_akid", Pattern: regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{Kind: "brave", Pattern: regexp.MustCompile(`BSA[A-Za-z0-9_\-]{25,}`)},
	{Kind: "dt0c01", Pattern: regexp.MustCompile(`dt0c01\.[A-Za-z0-9_.\-]{20,}`)},
	{Kind: "dt0s16", Pattern: regexp.MustCompile(`dt0s16\.[A-Za-z0-9_.\-]{20,}`)},
	{Kind: "gh_token", Pattern: regexp.MustCompile(`gh[psoru]_[A-Za-z0-9]{30,}`)},
	{Kind: "jwt", Pattern: regexp.MustCompile(`eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+`)},
}

// ellipsis is U+2026 HORIZONTAL ELLIPSIS, encoded UTF-8 (0xE2 0x80 0xA6)
// to match bash's literal `"\xE2\x80\xA6"` in the replacement template.
const ellipsis = "…"

// Apply runs every pattern (and the optional gateway-token literal)
// against input, returning the redacted text and the per-match hit
// list. The hit list is sorted by (offset_hint, kind).
func Apply(input string, opts Options) Result {
	if opts.Disable {
		return Result{Text: input}
	}

	var hits []Hit
	text := input

	for _, pk := range patternKinds {
		re := pk.Pattern
		kind := pk.Kind
		// Offsets are recorded against the *original* input so they
		// stay stable across later replacements.
		for _, idx := range re.FindAllStringIndex(input, -1) {
			hits = append(hits, Hit{Kind: kind, OffsetHint: idx[0], Count: 1})
		}
		text = re.ReplaceAllStringFunc(text, func(m string) string {
			head := m
			if len(head) > 11 {
				head = head[:11]
			}
			return "<REDACTED:" + kind + ":" + head + ellipsis + ">"
		})
	}

	if len(opts.GwToken) >= 16 {
		gw := opts.GwToken
		for off := 0; ; {
			idx := strings.Index(input[off:], gw)
			if idx < 0 {
				break
			}
			hits = append(hits, Hit{Kind: "gw_token", OffsetHint: off + idx, Count: 1})
			off += idx + len(gw)
		}
		head := gw
		if len(head) > 6 {
			head = head[:6]
		}
		text = strings.ReplaceAll(text, gw, "<REDACTED:gw_token:"+head+ellipsis+">")
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].OffsetHint != hits[j].OffsetHint {
			return hits[i].OffsetHint < hits[j].OffsetHint
		}
		return hits[i].Kind < hits[j].Kind
	})

	return Result{Text: text, Hits: hits}
}

// MarshalSink serialises hits to the byte-exact JSON shape the bash
// _redact helper writes to $CLAWCTL_REDACT_SINK. Field order is fixed
// (kind, offset_hint, count); count is hardcoded to 1 per hit so the
// sum equals the entry count, matching bash.
func MarshalSink(hits []Hit) []byte {
	if len(hits) == 0 {
		return []byte("[]")
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, h := range hits {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"kind":"%s","offset_hint":%d,"count":%d}`, h.Kind, h.OffsetHint, h.Count)
	}
	b.WriteByte(']')
	return []byte(b.String())
}

// AppendAudit writes one line to path matching bash's audit format:
//
//	<ISO-UTC> agent=<agent> kinds=<comma-joined-sorted>
//
// Returns nil and writes nothing when kinds is empty (matches bash's
// `if (%hits)` guard). The file is opened append-only and created if
// missing; audit failure is non-fatal at the call site.
func AppendAudit(path, agent string, kinds []string) error {
	if len(kinds) == 0 {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	ts := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	_, err = fmt.Fprintf(f, "%s agent=%s kinds=%s\n", ts, agent, strings.Join(kinds, ","))
	return err
}

// WarnLine returns the stderr WARNING formatted exactly as the bash
// helper writes it (with no trailing newline; the caller adds one).
func WarnLine(agent string, kinds []string) string {
	return fmt.Sprintf(
		"WARNING: redacted secret pattern(s): %s (agent=%s). Likely R-11 violation upstream; consider rotating the matching credential.",
		strings.Join(kinds, ","), agent,
	)
}
