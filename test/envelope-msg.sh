#!/usr/bin/env bash
# envelope-msg.sh — assert that `clawctl msg --envelope AGENT TEXT` emits a
# v1 ToolResponse JSON document on stdout that validates against
# schemas/envelope.v1.json, while plain `clawctl msg` (no flag) still emits
# the legacy text-only output.
#
# Strategy: spin up a Python http.server on a free localhost port that returns
# a canned OpenAI-compatible /v1/chat/completions response. Shadow `security`
# on PATH with a stub that hands `_token` a fake bearer so we never touch the
# real Keychain. Then run the bash wrapper twice (envelope + plain) and assert:
#   1. envelope output validates against the v1 schema (ajv-cli).
#   2. envelope carries: agent, traceparent, input, usage, finish_reason,
#      envelope_version="1".
#   3. plain `clawctl msg` returns the unwrapped content string.
#
# Bootstrap: needs python3, npx (for ajv-cli), curl, jq. Same policy as
# test/validate-fixtures.sh — fail fast with a usage error if missing.
#
# Exit 0 on pass, 1 on assertion fail, 2 on bootstrap miss.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="${BIN:-$ROOT/clawctl.bash}"
SCHEMA="$ROOT/schemas/envelope.v1.json"

if [[ ! -x "$BIN" ]]; then
  echo "FAIL: $BIN not executable" >&2; exit 1
fi

# bash wrapper: --envelope activates envelope output; no flag = text-only.
# Go binary:   envelope is the default; --text activates text-only output.
if [[ "$BIN" == *.bash ]]; then
  ENVELOPE_FLAG="--envelope"
  TEXT_FLAG=""
else
  ENVELOPE_FLAG=""
  TEXT_FLAG="--text"
fi
if [[ ! -f "$SCHEMA" ]]; then
  echo "FAIL: schema not found at $SCHEMA" >&2; exit 2
fi

for dep in python3 npx curl jq; do
  if ! command -v "$dep" >/dev/null 2>&1; then
    echo "FAIL: $dep is required to run this test" >&2; exit 2
  fi
done

TMP="$(mktemp -d)"
SERVER_PID=""

trap '
  if [[ -n "$SERVER_PID" ]]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf "$TMP"
' EXIT

fail=0
pass=0
ok()    { echo "  ok    $*"; pass=$((pass + 1)); }
fail_() { echo "  FAIL  $*" >&2; fail=$((fail + 1)); }

# Pick a free local port (race-y but acceptable for a local test).
PORT=$(python3 -c 'import socket
s = socket.socket(); s.bind(("127.0.0.1", 0))
print(s.getsockname()[1]); s.close()')

cat >"$TMP/gateway.py" <<'PY'
import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

CANNED = {
    "id": "chatcmpl-test",
    "object": "chat.completion",
    "created": 0,
    "model": "openclaw/default",
    "choices": [{
        "index": 0,
        "message": {"role": "assistant", "content": "hello world"},
        "finish_reason": "stop",
    }],
    "usage": {
        "prompt_tokens": 4,
        "completion_tokens": 2,
        "total_tokens": 6,
    },
}

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path == "/v1/chat/completions":
            length = int(self.headers.get("Content-Length", "0"))
            self.rfile.read(length)
            body = json.dumps(CANNED).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        self.send_response(404); self.end_headers()

    def log_message(self, *a, **k):
        pass

HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
PY

python3 "$TMP/gateway.py" "$PORT" &
SERVER_PID=$!

# Wait up to ~5s for the gateway to bind. Any HTTP code (incl. 404) means up.
ready=0
for _ in $(seq 1 50); do
  code=$(curl -s -o /dev/null --max-time 1 -w '%{http_code}' "http://127.0.0.1:$PORT/" || true)
  if [[ "$code" != "000" && -n "$code" ]]; then
    ready=1; break
  fi
  sleep 0.1
done
if [[ "$ready" -ne 1 ]]; then
  echo "FAIL: fake gateway did not come up on port $PORT" >&2
  exit 1
fi

# Stub `security` so `_token` returns a fake bearer without touching Keychain.
cat >"$TMP/security" <<'EOF'
#!/usr/bin/env bash
# Stub for `security find-generic-password ... -w` — emits a fake token.
echo "fake-token-for-tests"
EOF
chmod +x "$TMP/security"

run_clawctl() {
  PATH="$TMP:$PATH" \
  CLAWCTL_HOST="http://127.0.0.1:$PORT" \
  CLAWCTL_CACHE_DIR="$TMP/cache" \
  CLAWCTL_KEYCHAIN_SERVICE="openclaw-gateway-token" \
  "$BIN" "$@"
}

#───────────────────────────────────────────────────────────────────────────────
# Test 1: envelope output validates and carries required fields
#───────────────────────────────────────────────────────────────────────────────

echo "→ Test 1: clawctl msg --envelope emits a valid ToolResponse"
out_file="$TMP/envelope.json"
err_file="$TMP/envelope.err"
set +e
run_clawctl msg ${ENVELOPE_FLAG:+"$ENVELOPE_FLAG"} default "hello" >"$out_file" 2>"$err_file"
ec=$?
set -e

if [[ "$ec" -ne 0 ]]; then
  fail_ "clawctl msg --envelope exited $ec"
  sed 's/^/      /' <"$err_file" >&2 || true
fi

if npx --yes ajv-cli@5 validate --spec=draft2020 -s "$SCHEMA" -d "$out_file" >/dev/null 2>&1; then
  ok "envelope validates against envelope.v1.json"
else
  fail_ "envelope did not validate"
  npx --yes ajv-cli@5 validate --spec=draft2020 -s "$SCHEMA" -d "$out_file" >&2 || true
  sed 's/^/      /' <"$out_file" >&2 || true
fi

assert_eq() {
  local label="$1" expected="$2" actual="$3"
  if [[ "$actual" == "$expected" ]]; then
    ok "$label=$expected"
  else
    fail_ "$label=$actual (expected $expected)"
  fi
}

assert_eq "envelope_version" "1"                 "$(jq -r '.envelope_version' "$out_file")"
assert_eq "kind"              "tool_response"    "$(jq -r '.kind' "$out_file")"
assert_eq "agent"             "openclaw/default" "$(jq -r '.agent' "$out_file")"
assert_eq "input.content"     "hello"            "$(jq -r '.input.content' "$out_file")"
assert_eq "usage.input_tokens"  "4"              "$(jq -r '.usage.input_tokens' "$out_file")"
assert_eq "usage.output_tokens" "2"              "$(jq -r '.usage.output_tokens' "$out_file")"
assert_eq "usage.total_tokens"  "6"              "$(jq -r '.usage.total_tokens' "$out_file")"
assert_eq "finish_reason"     "stop"             "$(jq -r '.finish_reason' "$out_file")"

tp=$(jq -r '.traceparent' "$out_file")
if [[ "$tp" =~ ^00-[0-9a-f]{32}-[0-9a-f]{16}-01$ ]]; then
  ok "traceparent matches W3C shape"
else
  fail_ "traceparent=$tp"
fi

# trace-id should still surface on stderr (design principle 3).
if grep -Eq '^trace-id: [0-9a-f]{32}$' "$err_file"; then
  ok "trace-id printed to stderr"
else
  fail_ "trace-id missing from stderr"
fi

#───────────────────────────────────────────────────────────────────────────────
# Test 2: plain `clawctl msg` (no flag) preserves text-only output
#───────────────────────────────────────────────────────────────────────────────

echo "→ Test 2: plain clawctl msg preserves text-only output"
plain_file="$TMP/plain.txt"
set +e
run_clawctl msg ${TEXT_FLAG:+"$TEXT_FLAG"} default "hello" >"$plain_file" 2>/dev/null
ec=$?
set -e

if [[ "$ec" -ne 0 ]]; then
  fail_ "plain clawctl msg exited $ec"
fi

# Trim trailing newline for comparison.
plain_content=$(printf '%s' "$(cat "$plain_file")")
if [[ "$plain_content" == "hello world" ]]; then
  ok "plain msg returns the content string"
else
  fail_ "plain msg returned: <<$plain_content>>"
fi

# Ensure plain output is NOT a JSON object with envelope_version.
if jq -e 'type == "object" and has("envelope_version")' "$plain_file" >/dev/null 2>&1; then
  fail_ "plain msg unexpectedly emitted an envelope"
else
  ok "plain msg is not a JSON envelope"
fi

#───────────────────────────────────────────────────────────────────────────────

echo
if (( fail == 0 )); then
  echo "✓ $pass checks passed"
  exit 0
fi
echo "✗ $fail checks failed ($pass passed)" >&2
exit 1
