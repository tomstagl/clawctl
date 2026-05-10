#!/usr/bin/env bash
# smoke.sh — minimal validation against a real gateway.
# Requires: CLAWCTL_HOST set, keychain entry present.
# Usage: ./test/smoke.sh [--no-network]

set -euo pipefail

NO_NETWORK=0
for arg in "$@"; do
  case "$arg" in
    --no-network) NO_NETWORK=1 ;;
    *) echo "Unknown argument: $arg" >&2; exit 2 ;;
  esac
done

if [[ "$NO_NETWORK" -eq 1 ]] || [[ -z "${CLAWCTL_HOST:-}" ]]; then
  echo "skipping live tests (no CLAWCTL_HOST set)" >&2
  exit 0
fi

BIN="${BIN:-$(cd "$(dirname "$0")/.." && pwd)/clawctl.bash}"

if [[ ! -x "$BIN" ]]; then
  echo "FAIL: $BIN not executable" >&2
  exit 1
fi

echo "→ clawctl health"
"$BIN" health >/dev/null
echo "  ok"

echo "→ clawctl models (cached)"
"$BIN" models >/dev/null
echo "  ok"

echo "→ traceparent format"
trace_line=$({ "$BIN" raw GET /health >/dev/null; } 2>&1 | grep -E '^trace-id: [0-9a-f]{32}$' || true)
if [[ -z "$trace_line" ]]; then
  echo "FAIL: no traceparent printed to stderr" >&2
  exit 1
fi
echo "  ok ($trace_line)"

echo "→ redactor masks dt0c01"
masked=$(printf 'leak: dt0c01.AAAAAAAAAAAAAAAAAAAAAAAAAAAA.BBBBBBBBBBBBBBBB\n' \
  | "$BIN" raw POST /v1/dummy --data @- 2>/dev/null || true)
if echo "$masked" | grep -q 'dt0c01\.AAAAAAAA'; then
  echo "FAIL: redactor did not mask dt0c01 leak" >&2
  exit 1
fi
echo "  ok"

echo
echo "✓ smoke tests passed"
