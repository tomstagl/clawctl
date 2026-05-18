#!/usr/bin/env bash
# parity-redact.sh — diff the bash _redact perl pipeline against the Go
# internal/redact port across a fixture set covering every documented
# pattern (US-017).
#
# Coverage (20 fixtures):
#   1.  empty input
#   2.  no-secret input (passes through verbatim)
#   3.  single AWS access key id
#   4.  single Brave search key
#   5.  single Dynatrace dt0c01 token
#   6.  single Dynatrace dt0s16 token
#   7.  single GitHub personal access token (ghp_…)
#   8.  single GitHub server-to-server token (ghs_…)
#   9.  single GitHub user-to-server token (ghu_…)
#   10. single GitHub OAuth token (gho_…)
#   11. single GitHub refresh token (ghr_…)
#   12. single JWT
#   13. multiple kinds in one input (interleaved offsets)
#
# Fixture caveat: every JWT below uses an >= 8-char header part. The
# bash _redact's `while $text =~ s/.../e` substitution loop re-scans
# from position 0 after each replacement, which spins forever if the
# `<REDACTED:jwt:<first-11-chars-of-match>…>` replacement itself
# contains a JWT-shaped substring. Keeping the header at >= 8 chars
# guarantees the first 11 chars of the match contain no dot, so the
# replacement can't re-match. The Go port does a single-pass
# substitution and is immune; this constraint exists only to keep
# parity tests from hanging on bash.
#   14. same kind appearing twice
#   15. secret embedded in JSON value
#   16. secret across multiple lines
#   17. multibyte UTF-8 (λ + Greek) bracketing the secret
#   18. literal gateway token (>= 16 bytes) appearing twice
#   19. CLAWCTL_NO_REDACT=1 bypass — text passes through, sink writes []
#   20. all kinds in one input, plus a gw_token literal
#
# For each fixture we assert:
#   - stdout from bash and Go is byte-identical
#   - the CLAWCTL_REDACT_SINK JSON file is byte-identical
#   - exit codes match (always 0; redaction is not a transport failure)
#   - the audit-file line shape matches when matches occur (the timestamp
#     differs across invocations, so we compare the suffix only)
#
# We invoke the hidden `_redact` subcommand on both binaries — added in
# US-017 specifically to give parity tests a stable surface without
# coupling the test to msg/stream wiring (which lands later).
#
# Like other parity tests, this stashes a one-shot keychain item: bash's
# _redact calls `security find-generic-password` to fetch the gateway
# token literal, so we need a real (or absent — we test both) entry. On
# non-macOS hosts the test skips.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BASH_BIN="$ROOT/clawctl.bash"
TMP="$(mktemp -d)"
# Build the Go binary inside the repo (a sibling of the bash entrypoint)
# rather than under /tmp: macOS gatekeeper has been observed to reject
# /tmp-rooted Mach-O binaries with `missing LC_UUID load command` on
# some hosts, while the same binary built under the project tree runs
# fine. Tracked via test/parity-models.sh, which uses the same dodge.
GO_BIN_DIR="$ROOT/.test-bin"
GO_BIN="$GO_BIN_DIR/clawctl-go-redact"
KEYCHAIN_SVC="clawctl-parity-redact-$$"
KEYCHAIN_ADDED=0
GW_TOKEN="abcdef0123456789xyz" # 19 bytes, > 16 bound

cleanup() {
  if [[ "$KEYCHAIN_ADDED" -eq 1 ]]; then
    security delete-generic-password -s "$KEYCHAIN_SVC" -a "$USER" >/dev/null 2>&1 || true
  fi
  rm -rf "$TMP"
}
trap cleanup EXIT

if [[ ! -x "$BASH_BIN" ]]; then
  echo "FAIL: $BASH_BIN not executable" >&2
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  echo "FAIL: go toolchain not on PATH" >&2
  exit 1
fi

if ! command -v security >/dev/null 2>&1; then
  echo "SKIP: macOS \`security\` tool not available — bash _redact cannot fetch gw_token" >&2
  exit 0
fi

if ! command -v perl >/dev/null 2>&1; then
  echo "FAIL: perl not on PATH (bash _redact requires it)" >&2
  exit 1
fi

echo "building Go binary → $GO_BIN"
( cd "$ROOT" && CGO_ENABLED=0 go build -o "$GO_BIN" ./cmd/clawctl )

if security add-generic-password -s "$KEYCHAIN_SVC" -a "$USER" -w "$GW_TOKEN" >/dev/null 2>&1; then
  KEYCHAIN_ADDED=1
else
  echo "FAIL: could not add temporary keychain item" >&2
  exit 1
fi

fail=0; pass=0
ok()   { echo "  ok    $*"; pass=$((pass + 1)); }
nope() { echo "  FAIL  $*" >&2; fail=$((fail + 1)); }

# run_one $label $bin $agent $no_redact $input_file
# Drives one binary against one fixture, capturing stdout, sink JSON,
# audit-file content, and exit. Each invocation gets its own scratch
# CLAWCTL_CACHE_DIR + CLAWCTL_REDACT_SINK so fixtures can't stomp on
# each other's state.
#
# Input is read from $input_file rather than piped into run_one because
# bash runs functions on the right side of a pipe in a subshell — any
# `out_content=…` assignment would be discarded on return.
run_one() {
  local label="$1" bin="$2" agent="$3" no_redact="$4" input_file="$5"
  local cache="$TMP/$label.cache"
  local sink="$TMP/$label.sink.json"
  local out="$TMP/$label.stdout"
  local err="$TMP/$label.stderr"
  mkdir -p "$cache"
  rm -f "$sink" "$cache/last-redaction"

  set +e
  CLAWCTL_KEYCHAIN_SERVICE="$KEYCHAIN_SVC" \
  CLAWCTL_CACHE_DIR="$cache" \
  CLAWCTL_REDACT_SINK="$sink" \
  CLAWCTL_NO_REDACT="$no_redact" \
    "$bin" _redact "$agent" <"$input_file" >"$out" 2>"$err"
  exit_code=$?
  set -e
  out_content="$(cat "$out")"
  # stderr is captured to "$err" for post-mortem when a case fails but is
  # not asserted — the WARNING line shape is covered by the redact unit
  # tests, and bash can emit benign perl locale warnings on some hosts.
  : "$err"
  sink_content=""
  if [[ -f "$sink" ]]; then sink_content="$(cat "$sink")"; fi
  audit_content=""
  if [[ -f "$cache/last-redaction" ]]; then
    audit_content="$(cat "$cache/last-redaction")"
  fi
}

# diff_pair $name $input $agent $no_redact
# Runs the same fixture through bash and Go, compares stdout / sink /
# audit-suffix / exit, and increments pass or fail counters.
diff_pair() {
  local name="$1" input="$2" agent="$3" no_redact="${4:-0}"
  echo "case: $name"

  local infile="$TMP/$name.in"
  printf '%s' "$input" >"$infile"

  run_one "bash-$name" "$BASH_BIN" "$agent" "$no_redact" "$infile"
  local b_out="$out_content" b_sink="$sink_content" b_audit="$audit_content" b_exit="$exit_code"

  run_one "go-$name" "$GO_BIN" "$agent" "$no_redact" "$infile"
  local g_out="$out_content" g_sink="$sink_content" g_audit="$audit_content" g_exit="$exit_code"

  if [[ "$b_out" == "$g_out" ]]; then
    ok "stdout byte-identical"
  else
    nope "stdout diverged"
    diff <(printf '%s' "$b_out") <(printf '%s' "$g_out") || true
  fi

  if [[ "$b_sink" == "$g_sink" ]]; then
    ok "sink JSON byte-identical ($b_sink)"
  else
    nope "sink JSON diverged"
    diff <(printf '%s' "$b_sink") <(printf '%s' "$g_sink") || true
  fi

  if [[ "$b_exit" -eq "$g_exit" ]]; then
    ok "exit code $b_exit"
  else
    nope "exit codes differ: bash=$b_exit go=$g_exit"
  fi

  # Audit-file line: timestamp differs (each invocation has its own
  # `date` call), so trim the leading 20 chars (`YYYY-MM-DDTHH:MM:SSZ`)
  # before comparing the suffix.
  local b_suffix="${b_audit:20}" g_suffix="${g_audit:20}"
  if [[ "$b_suffix" == "$g_suffix" ]]; then
    ok "audit-file suffix byte-identical (${b_suffix:-<empty>})"
  else
    nope "audit-file suffix diverged"
    echo "    bash: $b_suffix"
    echo "    go:   $g_suffix"
  fi

  # Sanity: when there are matches, both must have written an audit line;
  # when there are none (or NO_REDACT bypass), neither should.
  if [[ -n "$b_audit" && -z "$g_audit" ]]; then
    nope "bash wrote audit but go did not"
  elif [[ -z "$b_audit" && -n "$g_audit" ]]; then
    nope "go wrote audit but bash did not"
  fi
}

#───────────────────────────────────────────────────────────────────────────
diff_pair "01-empty"          ""                                                          "rev"
diff_pair "02-no-secret"      "hello world\nno secrets here\n"                            "rev"
diff_pair "03-aws-akid"       "key=AKIAABCDEFGHIJKLMNOP done"                             "rev"
diff_pair "04-brave"          "x BSAaaaaaaaaaaaaaaaaaaaaaaaaa y"                          "rev"
diff_pair "05-dt0c01"         "tok=dt0c01.ABCDEFGHIJKLMNOPQRSTU end"                      "rev"
diff_pair "06-dt0s16"         "x dt0s16.0123456789012345678901234 y"                      "rev"
diff_pair "07-gh-ghp"         "ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa stop"                   "rev"
diff_pair "08-gh-ghs"         "ghs_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbb stop"                   "rev"
diff_pair "09-gh-ghu"         "ghu_cccccccccccccccccccccccccccccc stop"                   "rev"
diff_pair "10-gh-gho"         "gho_dddddddddddddddddddddddddddddd stop"                   "rev"
diff_pair "11-gh-ghr"         "ghr_eeeeeeeeeeeeeeeeeeeeeeeeeeeeee stop"                   "rev"
diff_pair "12-jwt"            "auth=eyJhbGciOiJIUzI1.eyJzdWIiOiJ4.signaturepart end"      "rev"
diff_pair "13-mixed-kinds"    "a AKIAABCDEFGHIJKLMNOP b ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa c eyJhbGciOiJIUzI1.eyJzdWIiOiJ4.signaturepart d" "rev"
diff_pair "14-dup-same-kind"  "AKIAABCDEFGHIJKLMNOP and AKIAZYXWVUTSRQPONMLK"             "rev"
diff_pair "15-json-embed"     '{"token":"AKIAABCDEFGHIJKLMNOP","ok":true}'                "rev"
diff_pair "16-multi-line"     "line1 AKIAABCDEFGHIJKLMNOP\nline2 ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\nline3" "rev"
diff_pair "17-utf8-bracket"   "λ AKIAABCDEFGHIJKLMNOP λ end"                              "rev"
diff_pair "18-gw-token-x2"    "before $GW_TOKEN middle $GW_TOKEN end"                     "rev"
diff_pair "19-no-redact"      "leak AKIAABCDEFGHIJKLMNOP through"                         "rev" "1"
diff_pair "20-everything"     "AKIAABCDEFGHIJKLMNOP BSAaaaaaaaaaaaaaaaaaaaaaaaaa dt0c01.ABCDEFGHIJKLMNOPQRSTU dt0s16.0123456789012345678901234 ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa eyJhbGciOiJIUzI1.eyJzdWIiOiJ4.signaturepart $GW_TOKEN" "rev"

#───────────────────────────────────────────────────────────────────────────
echo
echo "passed: $pass    failed: $fail"
[[ "$fail" -eq 0 ]] || exit 1
