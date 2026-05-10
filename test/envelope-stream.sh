#!/usr/bin/env bash
# envelope-stream.sh — assert that `clawctl stream --envelope AGENT TEXT`
# emits one v1 ToolStreamChunk NDJSON line per non-empty SSE delta followed by
# a terminal ToolResponse, with each line validating against
# schemas/envelope.v1.json. Plain `clawctl stream` (no flag) must still emit
# the legacy buffered+redacted text-only output.
#
# Strategy: spin up a Python http.server on a free localhost port that returns
# a canned OpenAI-compatible /v1/chat/completions SSE response with three
# content deltas + a finish frame. Shadow `security` on PATH with a stub so
# `_token` returns a fake bearer (no Keychain access). Run the bash wrapper
# twice (envelope + plain) and assert:
#   1. envelope output is NDJSON; every line validates against the v1 schema.
#   2. last line is a ToolResponse with finish_reason set; prior lines are
#      ToolStreamChunks; chunk indices are 0..N-1.
#   3. every line carries the same traceparent (one trace per tool call).
#   4. plain `clawctl stream` returns the unwrapped buffered text.
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

PORT=$(python3 -c 'import socket
s = socket.socket(); s.bind(("127.0.0.1", 0))
print(s.getsockname()[1]); s.close()')

# Fake gateway emitting a multi-chunk SSE response on POST /v1/chat/completions.
# Three content deltas + a finish frame + [DONE], matching the OpenAI SSE shape
# the bash wrapper expects.
cat >"$TMP/gateway.py" <<'PY'
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

CHUNKS = [
    b'data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"Hello "}}]}\n\n',
    b'data: {"choices":[{"index":0,"delta":{"content":"streamed "}}]}\n\n',
    b'data: {"choices":[{"index":0,"delta":{"content":"world."}}]}\n\n',
    b'data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],'
    b'"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}\n\n',
    b'data: [DONE]\n\n',
]

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path != "/v1/chat/completions":
            self.send_response(404); self.end_headers(); return
        length = int(self.headers.get("Content-Length", "0"))
        self.rfile.read(length)
        body = b"".join(CHUNKS)
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *a, **k): pass

HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
PY

python3 "$TMP/gateway.py" "$PORT" &
SERVER_PID=$!

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
# Test 1: --envelope emits NDJSON: N ToolStreamChunks + 1 ToolResponse,
# every line validates, indices are 0..N-1, traceparent is shared.
#───────────────────────────────────────────────────────────────────────────────

echo "→ Test 1: clawctl stream --envelope emits valid NDJSON"
out_file="$TMP/envelope.ndjson"
err_file="$TMP/envelope.err"
set +e
run_clawctl stream ${ENVELOPE_FLAG:+"$ENVELOPE_FLAG"} default "say hi" >"$out_file" 2>"$err_file"
ec=$?
set -e

if [[ "$ec" -ne 0 ]]; then
  fail_ "clawctl stream --envelope exited $ec"
  sed 's/^/      /' <"$err_file" >&2 || true
fi

# Validate each line standalone.
line_no=0
all_valid=1
chunk_count=0
last_kind=""
seen_indices=()
seen_tps=()
while IFS= read -r line || [[ -n "$line" ]]; do
  line_no=$((line_no + 1))
  [[ -z "$line" ]] && continue
  frag="$TMP/line.$line_no.json"
  printf '%s\n' "$line" > "$frag"
  if ! npx --yes ajv-cli@5 validate --spec=draft2020 -s "$SCHEMA" -d "$frag" >/dev/null 2>&1; then
    fail_ "line $line_no does not validate against envelope.v1.json"
    npx --yes ajv-cli@5 validate --spec=draft2020 -s "$SCHEMA" -d "$frag" >&2 || true
    all_valid=0
  fi
  kind=$(jq -r '.kind' "$frag")
  last_kind="$kind"
  if [[ "$kind" == "tool_stream_chunk" ]]; then
    chunk_count=$((chunk_count + 1))
    seen_indices+=("$(jq -r '.index' "$frag")")
  fi
  seen_tps+=("$(jq -r '.traceparent' "$frag")")
done < "$out_file"

if (( all_valid == 1 )); then
  ok "every NDJSON line validates against envelope.v1.json"
fi

if (( chunk_count >= 1 )); then
  ok "emitted $chunk_count tool_stream_chunk line(s)"
else
  fail_ "no tool_stream_chunk lines emitted (got $chunk_count)"
fi

if [[ "$last_kind" == "tool_response" ]]; then
  ok "terminal frame is tool_response"
else
  fail_ "terminal frame is '$last_kind', expected tool_response"
fi

# Final ToolResponse must carry a finish_reason from the FinishReason enum.
finish=$(tail -n 1 "$out_file" | jq -r '.finish_reason // ""')
case "$finish" in
  stop|length|content_filter|tool_call|error)
    ok "tool_response.finish_reason='$finish'" ;;
  *)
    fail_ "tool_response.finish_reason='$finish' is not in the FinishReason enum" ;;
esac

# Chunk indices should be 0..N-1, strictly increasing.
expected=0
indices_ok=1
for i in "${seen_indices[@]}"; do
  if [[ "$i" != "$expected" ]]; then
    indices_ok=0
    fail_ "chunk indices not 0..N-1: saw '$i' at position $expected"
    break
  fi
  expected=$((expected + 1))
done
if (( indices_ok == 1 )); then
  ok "chunk indices are 0..$((chunk_count - 1))"
fi

# Every line must share the same traceparent (one trace per tool call).
first_tp="${seen_tps[0]}"
tps_match=1
for t in "${seen_tps[@]}"; do
  if [[ "$t" != "$first_tp" ]]; then
    tps_match=0
    fail_ "traceparent drift: '$t' != '$first_tp'"
    break
  fi
done
if (( tps_match == 1 )); then
  if [[ "$first_tp" =~ ^00-[0-9a-f]{32}-[0-9a-f]{16}-01$ ]]; then
    ok "shared traceparent matches W3C shape"
  else
    fail_ "traceparent='$first_tp' does not match W3C shape"
  fi
fi

# trace-id should still surface on stderr (design principle 3).
if grep -Eq '^trace-id: [0-9a-f]{32}$' "$err_file"; then
  ok "trace-id printed to stderr"
else
  fail_ "trace-id missing from stderr"
fi

# Output text on the terminal frame should equal the concatenated deltas.
output=$(tail -n 1 "$out_file" | jq -r '.output')
if [[ "$output" == "Hello streamed world." ]]; then
  ok "tool_response.output is the redacted aggregate"
else
  fail_ "tool_response.output=<<$output>>"
fi

# Usage echoed from the gateway frame must be carried through.
in_tok=$(tail -n 1 "$out_file" | jq -r '.usage.input_tokens')
out_tok=$(tail -n 1 "$out_file" | jq -r '.usage.output_tokens')
tot_tok=$(tail -n 1 "$out_file" | jq -r '.usage.total_tokens')
if [[ "$in_tok" == "7" && "$out_tok" == "3" && "$tot_tok" == "10" ]]; then
  ok "tool_response.usage carries gateway-reported counts"
else
  fail_ "tool_response.usage in=$in_tok out=$out_tok tot=$tot_tok"
fi

#───────────────────────────────────────────────────────────────────────────────
# Test 2: plain `clawctl stream` (no flag) preserves text-only output.
#───────────────────────────────────────────────────────────────────────────────

echo "→ Test 2: plain clawctl stream preserves text-only output"
plain_file="$TMP/plain.txt"
set +e
run_clawctl stream ${TEXT_FLAG:+"$TEXT_FLAG"} default "say hi" >"$plain_file" 2>/dev/null
ec=$?
set -e

if [[ "$ec" -ne 0 ]]; then
  fail_ "plain clawctl stream exited $ec"
fi

plain_content=$(printf '%s' "$(cat "$plain_file")")
if [[ "$plain_content" == "Hello streamed world." ]]; then
  ok "plain stream returns the buffered concatenated content"
else
  fail_ "plain stream returned: <<$plain_content>>"
fi

# Plain output must NOT be a JSON document with envelope_version.
if jq -e 'type == "object" and has("envelope_version")' "$plain_file" >/dev/null 2>&1; then
  fail_ "plain stream unexpectedly emitted an envelope"
else
  ok "plain stream is not a JSON envelope"
fi

#───────────────────────────────────────────────────────────────────────────────

echo
if (( fail == 0 )); then
  echo "✓ $pass checks passed"
  exit 0
fi
echo "✗ $fail checks failed ($pass passed)" >&2
exit 1
