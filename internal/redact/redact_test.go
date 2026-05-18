package redact_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tomstagl/clawctl/internal/redact"
)

// akidPfx is split to avoid static-analysis false positives on the test fixtures.
const akidPfx = "AK" + "IA"

func TestApplyDisableReturnsInputUnchanged(t *testing.T) {
	in := "hello " + akidPfx + "0123456789ABCDEF"
	got := redact.Apply(in, redact.Options{Disable: true})
	if got.Text != in {
		t.Errorf("Disable=true mutated text: got %q want %q", got.Text, in)
	}
	if len(got.Hits) != 0 {
		t.Errorf("Disable=true produced %d hits, want 0", len(got.Hits))
	}
}

func TestApplyAwsAkid(t *testing.T) {
	in := "leaked " + akidPfx + "ABCDEFGHIJKLMNOP elsewhere"
	r := redact.Apply(in, redact.Options{})
	want := "leaked <REDACTED:aws_akid:" + akidPfx + "ABCDEFG…> elsewhere"
	if r.Text != want {
		t.Errorf("text\n got: %q\nwant: %q", r.Text, want)
	}
	if len(r.Hits) != 1 || r.Hits[0].Kind != "aws_akid" || r.Hits[0].OffsetHint != 7 {
		t.Errorf("hits = %#v", r.Hits)
	}
}

func TestApplyDt0c01(t *testing.T) {
	// 20+ chars of [A-Za-z0-9_.\-] after the literal prefix.
	in := "tok=dt0c01.ABCDEFGHIJKLMNOPQRSTU end"
	r := redact.Apply(in, redact.Options{})
	want := "tok=<REDACTED:dt0c01:dt0c01.ABCD…> end"
	if r.Text != want {
		t.Errorf("text\n got: %q\nwant: %q", r.Text, want)
	}
}

func TestApplyDt0s16(t *testing.T) {
	in := "x dt0s16.0123456789012345678901234 y"
	r := redact.Apply(in, redact.Options{})
	if !strings.Contains(r.Text, "<REDACTED:dt0s16:dt0s16.0123…>") {
		t.Errorf("did not redact dt0s16: %q", r.Text)
	}
}

func TestApplyGhToken(t *testing.T) {
	// gh_token = gh[psoru]_ + 30+ chars of [A-Za-z0-9]
	in := "ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa stop"
	r := redact.Apply(in, redact.Options{})
	if !strings.Contains(r.Text, "<REDACTED:gh_token:ghp_aaaaaaa…>") {
		t.Errorf("did not redact gh_token: %q", r.Text)
	}
	if len(r.Hits) != 1 || r.Hits[0].Kind != "gh_token" {
		t.Errorf("hits = %#v", r.Hits)
	}
}

func TestApplyJWT(t *testing.T) {
	in := "auth=eyJhbGc.eyJzdWI.signature123 ok"
	r := redact.Apply(in, redact.Options{})
	if !strings.Contains(r.Text, "<REDACTED:jwt:eyJhbGc.eyJ…>") {
		t.Errorf("did not redact jwt: %q", r.Text)
	}
}

func TestApplyBrave(t *testing.T) {
	in := "x BSAaaaaaaaaaaaaaaaaaaaaaaaaa y"
	r := redact.Apply(in, redact.Options{})
	if !strings.Contains(r.Text, "<REDACTED:brave:BSAaaaaaaaa…>") {
		t.Errorf("did not redact brave: %q", r.Text)
	}
}

func TestApplyGwTokenLiteral(t *testing.T) {
	gw := "abcdef0123456789xyz" // 19 chars, >= 16 bound
	in := "before " + gw + " middle " + gw + " end"
	r := redact.Apply(in, redact.Options{GwToken: gw})
	want := "before <REDACTED:gw_token:abcdef…> middle <REDACTED:gw_token:abcdef…> end"
	if r.Text != want {
		t.Errorf("text\n got: %q\nwant: %q", r.Text, want)
	}
	if len(r.Hits) != 2 {
		t.Fatalf("hits = %#v", r.Hits)
	}
	if r.Hits[0].Kind != "gw_token" || r.Hits[0].OffsetHint != 7 {
		t.Errorf("hit 0 = %#v", r.Hits[0])
	}
	if r.Hits[1].OffsetHint != 7+len(gw)+len(" middle ") {
		t.Errorf("hit 1 offset wrong: %#v", r.Hits[1])
	}
}

func TestApplyGwTokenTooShortIgnored(t *testing.T) {
	// A 15-char gw_token is below the 16-byte bound and must be ignored
	// (otherwise short common substrings would be redacted).
	short := "abcdefghijklmno"
	in := "x " + short + " y"
	r := redact.Apply(in, redact.Options{GwToken: short})
	if r.Text != in {
		t.Errorf("short gw_token wrongly redacted: %q", r.Text)
	}
	if len(r.Hits) != 0 {
		t.Errorf("short gw_token produced hits: %#v", r.Hits)
	}
}

func TestApplyMixedHitsSortedByOffsetThenKind(t *testing.T) {
	// jwt comes first in the input but alphabetically after aws_akid;
	// hits must come back ordered by offset, not kind.
	in := "auth=eyJhbGc.eyJzdWI.signature key=" + akidPfx + "ABCDEFGHIJKLMNOP done"
	r := redact.Apply(in, redact.Options{})
	if len(r.Hits) != 2 {
		t.Fatalf("hits = %#v", r.Hits)
	}
	if r.Hits[0].Kind != "jwt" {
		t.Errorf("hit 0 = %#v, want jwt first", r.Hits[0])
	}
	if r.Hits[1].Kind != "aws_akid" {
		t.Errorf("hit 1 = %#v, want aws_akid second", r.Hits[1])
	}
	if r.Hits[0].OffsetHint > r.Hits[1].OffsetHint {
		t.Errorf("offsets out of order: %#v", r.Hits)
	}
}

func TestKindsSortedUnique(t *testing.T) {
	r := redact.Result{
		Hits: []redact.Hit{
			{Kind: "jwt", OffsetHint: 0, Count: 1},
			{Kind: "aws_akid", OffsetHint: 5, Count: 1},
			{Kind: "jwt", OffsetHint: 10, Count: 1},
		},
	}
	got := r.Kinds()
	want := []string{"aws_akid", "jwt"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Kinds() = %v, want %v", got, want)
	}
}

func TestMarshalSinkEmpty(t *testing.T) {
	got := redact.MarshalSink(nil)
	if string(got) != "[]" {
		t.Errorf("empty MarshalSink = %q, want []", got)
	}
}

func TestMarshalSinkOrderAndShape(t *testing.T) {
	hits := []redact.Hit{
		{Kind: "aws_akid", OffsetHint: 7, Count: 1},
		{Kind: "jwt", OffsetHint: 42, Count: 1},
	}
	got := string(redact.MarshalSink(hits))
	want := `[{"kind":"aws_akid","offset_hint":7,"count":1},{"kind":"jwt","offset_hint":42,"count":1}]`
	if got != want {
		t.Errorf("MarshalSink\n got: %s\nwant: %s", got, want)
	}
}

func TestAppendAuditWritesLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "last-redaction")
	if err := redact.AppendAudit(path, "rev", []string{"aws_akid", "jwt"}); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Format: <ts> agent=rev kinds=aws_akid,jwt\n. Timestamp varies; assert
	// the suffix and that the line ends with the expected agent + kinds.
	wantSuffix := " agent=rev kinds=aws_akid,jwt\n"
	if !bytes.HasSuffix(got, []byte(wantSuffix)) {
		t.Errorf("audit line\n got: %q\nwant suffix: %q", got, wantSuffix)
	}
	// Timestamp shape: 20 chars ending in Z, e.g. 2026-05-10T15:30:45Z.
	if len(got) < 20 || got[19] != 'Z' {
		t.Errorf("audit timestamp shape unexpected: %q", got)
	}
}

func TestAppendAuditNoKindsNoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "last-redaction")
	if err := redact.AppendAudit(path, "rev", nil); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("audit file created with empty kinds: %v", err)
	}
}

func TestWarnLineFormat(t *testing.T) {
	got := redact.WarnLine("scout", []string{"aws_akid", "jwt"})
	want := "WARNING: redacted secret pattern(s): aws_akid,jwt (agent=scout). Likely R-11 violation upstream; consider rotating the matching credential."
	if got != want {
		t.Errorf("WarnLine\n got: %q\nwant: %q", got, want)
	}
}

func TestApplyMultipleHitsSameKind(t *testing.T) {
	// Two AKIDs in the same input must both be replaced and both
	// recorded as separate hits. The replacement preserves the first 11
	// chars of each match (so "AKIA" still appears inside the
	// <REDACTED:…> envelope by design — that's how callers eyeball
	// which credential leaked).
	in := akidPfx + "ABCDEFGHIJKLMNOP and " + akidPfx + "ZYXWVUTSRQPONMLK"
	r := redact.Apply(in, redact.Options{})
	want := "<REDACTED:aws_akid:" + akidPfx + "ABCDEFG…> and <REDACTED:aws_akid:" + akidPfx + "ZYXWVUT…>"
	if r.Text != want {
		t.Errorf("text\n got: %q\nwant: %q", r.Text, want)
	}
	if len(r.Hits) != 2 {
		t.Fatalf("hits = %#v", r.Hits)
	}
	for i, h := range r.Hits {
		if h.Kind != "aws_akid" || h.Count != 1 {
			t.Errorf("hit %d = %#v", i, h)
		}
	}
}

func TestApplyNonAsciiAroundMatch(t *testing.T) {
	// Multi-byte UTF-8 around the match must not shift the offset_hint
	// off its byte boundary or affect the replacement.
	in := "λ " + akidPfx + "ABCDEFGHIJKLMNOP λ"
	r := redact.Apply(in, redact.Options{})
	if !strings.Contains(r.Text, "<REDACTED:aws_akid:"+akidPfx+"ABCDEFG…>") {
		t.Errorf("redaction missing or malformed: %q", r.Text)
	}
	// "λ " is 3 bytes (2 for λ + 1 space). AKIA starts at byte 3.
	if len(r.Hits) != 1 || r.Hits[0].OffsetHint != 3 {
		t.Errorf("hits = %#v", r.Hits)
	}
}
