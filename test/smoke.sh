#!/usr/bin/env bash
# smoke.sh — minimal validation against a real gateway.
# Requires: CLAWCTL_HOST set, keychain entry present.

set -euo pipefail

BIN="${BIN:-$(cd "$(dirname "$0")/.." && pwd)/clawctl}"

if [[ ! -x "$BIN" ]]; then
  echo "FAIL: $BIN not executable" >&2
  exit 1
fi

if [[ -z "${CLAWCTL_HOST:-}" ]]; then
  echo "SKIP: CLAWCTL_HOST not set" >&2
  exit 0
fi

echo "→ clawctl health"
"$BIN" health >/dev/null
echo "  ok"

echo "→ clawctl models (cached)"
"$BIN" models >/dev/null
echo "  ok"

echo "→ traceparent format"
trace_line=$("$BIN" raw GET /health 2>&1 1>/dev/null | grep -E '^trace-id: [0-9a-f]{32}$' || true)
if [[ -z "$trace_line" ]]; then
  echo "FAIL: no traceparent printed to stderr" >&2
  exit 1
fi
echo "  ok ($trace_line)"

echo "→ redactor masks dt0c01"
masked=$(echo "leak: dt0c01.AAAAAAAAAAAAAAAAAAAAAAAAAAAA.BBBBBBBBBBBBBBBB" \
  | "$BIN" raw POST /v1/dummy --data @- 2>/dev/null || true)
if echo "$masked" | grep -q 'dt0c01\.AAAAAAAA'; then
  echo "FAIL: redactor did not mask dt0c01 leak" >&2
  exit 1
fi
echo "  ok"

echo
echo "✓ smoke tests passed"
