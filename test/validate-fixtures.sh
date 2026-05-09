#!/usr/bin/env bash
# validate-fixtures.sh — run every envelope fixture through the v1 schema.
#
# Validator: ajv-cli with --spec=draft2020 (matches schemas/envelope.v1.json).
# Bootstrap policy: the script invokes ajv-cli via `npx --yes ajv-cli@5`. If
# `npx` is missing the script exits 2 (usage error) rather than silently
# skipping; offline runs should pre-warm the npx cache.
#
# NDJSON handling: streaming.ndjson is split line-by-line; each non-empty line
# is validated as a standalone document.
#
# Exit codes follow clawctl convention:
#   0 — every fixture validated
#   2 — usage/bootstrap error (no npx, schema/fixtures missing)
#   1 — at least one fixture failed validation

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCHEMA="$ROOT/schemas/envelope.v1.json"
FIX_DIR="$ROOT/test/fixtures/envelope"

if ! command -v npx >/dev/null 2>&1; then
  echo "FAIL: npx is required to run ajv-cli (install Node.js >=18)" >&2
  exit 2
fi

if [[ ! -f "$SCHEMA" ]]; then
  echo "FAIL: schema not found at $SCHEMA" >&2
  exit 2
fi

if [[ ! -d "$FIX_DIR" ]]; then
  echo "FAIL: fixtures dir not found at $FIX_DIR" >&2
  exit 2
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

ajv() {
  npx --yes ajv-cli@5 validate --spec=draft2020 -s "$SCHEMA" -d "$1"
}

fail=0
pass=0

validate_json() {
  local label="$1" path="$2"
  if ajv "$path" >/dev/null 2>&1; then
    echo "  ok    $label"
    pass=$((pass + 1))
  else
    echo "  FAIL  $label" >&2
    ajv "$path" >&2 || true
    fail=$((fail + 1))
  fi
}

validate_ndjson() {
  local label="$1" path="$2" line_no=0
  while IFS= read -r line || [[ -n "$line" ]]; do
    line_no=$((line_no + 1))
    [[ -z "$line" ]] && continue
    local frag="$TMP/$(basename "$path").$line_no.json"
    printf '%s\n' "$line" > "$frag"
    if ajv "$frag" >/dev/null 2>&1; then
      echo "  ok    $label#$line_no"
      pass=$((pass + 1))
    else
      echo "  FAIL  $label#$line_no" >&2
      ajv "$frag" >&2 || true
      fail=$((fail + 1))
    fi
  done < "$path"
}

echo "→ validating envelope fixtures against $(basename "$SCHEMA")"

[[ -f "$FIX_DIR/happy.json" ]]    || { echo "FAIL: happy.json missing"    >&2; exit 2; }
[[ -f "$FIX_DIR/error.json" ]]    || { echo "FAIL: error.json missing"    >&2; exit 2; }
[[ -f "$FIX_DIR/redacted.json" ]] || { echo "FAIL: redacted.json missing" >&2; exit 2; }
[[ -f "$FIX_DIR/streaming.ndjson" ]] || { echo "FAIL: streaming.ndjson missing" >&2; exit 2; }

validate_json "happy.json"     "$FIX_DIR/happy.json"
validate_json "error.json"     "$FIX_DIR/error.json"
validate_json "redacted.json"  "$FIX_DIR/redacted.json"
validate_ndjson "streaming.ndjson" "$FIX_DIR/streaming.ndjson"

# Streaming-specific structural checks: ≥3 chunks then a terminal ToolResponse.
chunk_count=$(grep -c '"kind":"tool_stream_chunk"' "$FIX_DIR/streaming.ndjson" || true)
last_kind=$(tail -n 1 "$FIX_DIR/streaming.ndjson" | python3 -c 'import json,sys; print(json.loads(sys.stdin.read()).get("kind",""))')

if (( chunk_count < 3 )); then
  echo "  FAIL  streaming.ndjson has $chunk_count tool_stream_chunk lines (need >=3)" >&2
  fail=$((fail + 1))
else
  echo "  ok    streaming.ndjson chunk count = $chunk_count"
  pass=$((pass + 1))
fi

if [[ "$last_kind" != "tool_response" ]]; then
  echo "  FAIL  streaming.ndjson terminal frame is '$last_kind', expected 'tool_response'" >&2
  fail=$((fail + 1))
else
  echo "  ok    streaming.ndjson terminates with tool_response"
  pass=$((pass + 1))
fi

# Redaction fixture must have a non-empty redactions[].
red_len=$(python3 -c 'import json; print(len(json.load(open("'"$FIX_DIR/redacted.json"'"))["redactions"]))')
if (( red_len < 1 )); then
  echo "  FAIL  redacted.json has empty redactions[]" >&2
  fail=$((fail + 1))
else
  echo "  ok    redacted.json redactions[] length = $red_len"
  pass=$((pass + 1))
fi

echo
if (( fail == 0 )); then
  echo "✓ $pass checks passed"
  exit 0
fi
echo "✗ $fail checks failed ($pass passed)" >&2
exit 1
