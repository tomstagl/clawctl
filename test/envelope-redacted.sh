#!/usr/bin/env bash
# envelope-redacted.sh — assert that --envelope surfaces redaction events in
# the envelope's redactions[] array (kind + offset_hint), while preserving
# the existing stderr WARNING and audit-file append (US-008).
#
# Strategy: fake gateway returns a /v1/chat/completions response whose content
# contains two known-secret-shaped strings. Run `clawctl msg --envelope` and
# `clawctl stream --envelope` against it and assert:
#   1. envelope output validates against schemas/envelope.v1.json.
#   2. envelope.redactions[] is non-empty and includes the expected kinds.
#   3. each redaction entry carries kind + offset_hint.
#   4. stderr still has the WARNING line (humans depend on it).
#   5. audit file at $CLAWCTL_CACHE_DIR/last-redaction is appended to.
#   6. plain `clawctl msg` (no flag) still emits redacted text (no envelope).
#
# Bootstrap: needs python3, npx (ajv-cli), curl, jq.
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

# Two distinct secret-shaped strings inside the response content. Patterns:
#   dt0c01.<>=20+ chars  -> kind: dt0c01
#   ghp_<>=30+ chars     -> kind: gh_token
SECRET_DT='dt0c01.ABCDEFGHIJKLMNOPQRSTUVWXYZ012345'
SECRET_GH='ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789'
CONTENT="here is a tenant token: ${SECRET_DT} and a github pat: ${SECRET_GH}, please rotate."

cat >"$TMP/gateway.py" <<'PY'
import json, os, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

CONTENT = os.environ["FAKE_CONTENT"]

CHAT = {
    "id": "chatcmpl-redact",
    "object": "chat.completion",
    "created": 0,
    "model": "openclaw/default",
    "choices": [{
        "index": 0,
        "message": {"role": "assistant", "content": CONTENT},
        "finish_reason": "stop",
    }],
    "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
}

# SSE: split the same content into two deltas plus a finish frame.
half = len(CONTENT) // 2
CHUNKS = [
    ('data: ' + json.dumps({"choices":[{"index":0,"delta":{"role":"assistant","content":CONTENT[:half]}}]}) + '\n\n').encode(),
    ('data: ' + json.dumps({"choices":[{"index":0,"delta":{"content":CONTENT[half:]}}]}) + '\n\n').encode(),
    ('data: ' + json.dumps({"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}) + '\n\n').encode(),
    b'data: [DONE]\n\n',
]

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path != "/v1/chat/completions":
            self.send_response(404); self.end_headers(); return
        length = int(self.headers.get("Content-Length", "0"))
        body_in = self.rfile.read(length)
        try:
            req = json.loads(body_in)
        except Exception:
            req = {}
        if req.get("stream"):
            body = b"".join(CHUNKS)
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        body = json.dumps(CHAT).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *a, **k): pass

HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
PY

FAKE_CONTENT="$CONTENT" python3 "$TMP/gateway.py" "$PORT" &
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

CACHE="$TMP/cache"
mkdir -p "$CACHE"

run_clawctl() {
  PATH="$TMP:$PATH" \
  CLAWCTL_HOST="http://127.0.0.1:$PORT" \
  CLAWCTL_CACHE_DIR="$CACHE" \
  CLAWCTL_KEYCHAIN_SERVICE="openclaw-gateway-token" \
  CLAWCTL_TOKEN_CMD="echo fake-token-for-tests" \
  "$BIN" "$@"
}

#───────────────────────────────────────────────────────────────────────────────
# Test 1: msg --envelope populates redactions[]
#───────────────────────────────────────────────────────────────────────────────

echo "→ Test 1: clawctl msg --envelope populates redactions[]"
out_file="$TMP/msg.json"
err_file="$TMP/msg.err"
rm -f "$CACHE/last-redaction"
set +e
run_clawctl msg ${ENVELOPE_FLAG:+"$ENVELOPE_FLAG"} default "echo my secrets" >"$out_file" 2>"$err_file"
ec=$?
set -e

if [[ "$ec" -ne 0 ]]; then
  fail_ "msg --envelope exited $ec"
  sed 's/^/      /' <"$err_file" >&2 || true
fi

if npx --yes ajv-cli@5 validate --spec=draft2020 -s "$SCHEMA" -d "$out_file" >/dev/null 2>&1; then
  ok "envelope validates against envelope.v1.json"
else
  fail_ "envelope did not validate"
  npx --yes ajv-cli@5 validate --spec=draft2020 -s "$SCHEMA" -d "$out_file" >&2 || true
  sed 's/^/      /' <"$out_file" >&2 || true
fi

red_count=$(jq '.redactions | length' "$out_file")
if [[ "$red_count" -ge 2 ]]; then
  ok "redactions[] has $red_count entries (>=2 expected)"
else
  fail_ "redactions[] has $red_count entries"
fi

if jq -e '[.redactions[].kind] | contains(["dt0c01"])' "$out_file" >/dev/null; then
  ok "redactions[] includes kind=dt0c01"
else
  fail_ "redactions[] missing kind=dt0c01"
fi

if jq -e '[.redactions[].kind] | contains(["gh_token"])' "$out_file" >/dev/null; then
  ok "redactions[] includes kind=gh_token"
else
  fail_ "redactions[] missing kind=gh_token"
fi

# Each entry must have kind and offset_hint (offset is integer >= 0).
if jq -e 'all(.redactions[]; has("kind") and (has("offset_hint") and (.offset_hint|type=="number") and .offset_hint>=0))' "$out_file" >/dev/null; then
  ok "every redaction has kind + integer offset_hint"
else
  fail_ "some redaction is missing kind/offset_hint"
fi

# Output text must be redacted (no raw secret leaks).
if jq -r '.output' "$out_file" | grep -qF "$SECRET_DT"; then
  fail_ "raw dt0c01 secret leaked into envelope.output"
else
  ok "raw dt0c01 secret absent from envelope.output"
fi
if jq -r '.output' "$out_file" | grep -qF "$SECRET_GH"; then
  fail_ "raw gh_token secret leaked into envelope.output"
else
  ok "raw gh_token secret absent from envelope.output"
fi

# Existing stderr WARNING line is still emitted (human path).
if grep -q '^WARNING: redacted secret pattern' "$err_file"; then
  ok "stderr WARNING is still emitted"
else
  fail_ "stderr WARNING missing from msg --envelope"
  sed 's/^/      /' <"$err_file" >&2 || true
fi

# Audit file in $CLAWCTL_CACHE_DIR/last-redaction is appended to.
if [[ -s "$CACHE/last-redaction" ]] \
   && grep -q 'agent=default' "$CACHE/last-redaction" \
   && grep -q 'kinds=.*dt0c01' "$CACHE/last-redaction" \
   && grep -q 'kinds=.*gh_token' "$CACHE/last-redaction"; then
  ok "audit file appended at $CACHE/last-redaction"
else
  fail_ "audit file not appended (or missing expected kinds)"
  if [[ -f "$CACHE/last-redaction" ]]; then
    sed 's/^/      /' <"$CACHE/last-redaction" >&2 || true
  fi
fi

#───────────────────────────────────────────────────────────────────────────────
# Test 2: stream --envelope populates redactions[] on chunk and/or terminal
#───────────────────────────────────────────────────────────────────────────────

echo "→ Test 2: clawctl stream --envelope populates redactions[]"
nd_file="$TMP/stream.ndjson"
err_file="$TMP/stream.err"
rm -f "$CACHE/last-redaction"
set +e
run_clawctl stream ${ENVELOPE_FLAG:+"$ENVELOPE_FLAG"} default "echo my secrets" >"$nd_file" 2>"$err_file"
ec=$?
set -e

if [[ "$ec" -ne 0 ]]; then
  fail_ "stream --envelope exited $ec"
  sed 's/^/      /' <"$err_file" >&2 || true
fi

# Each line must validate against the schema.
line_no=0
all_valid=1
total_red=0
seen_kinds=""
while IFS= read -r line || [[ -n "$line" ]]; do
  line_no=$((line_no + 1))
  [[ -z "$line" ]] && continue
  frag="$TMP/sline.$line_no.json"
  printf '%s\n' "$line" > "$frag"
  if ! npx --yes ajv-cli@5 validate --spec=draft2020 -s "$SCHEMA" -d "$frag" >/dev/null 2>&1; then
    fail_ "stream line $line_no does not validate"
    all_valid=0
  fi
  cnt=$(jq '.redactions // [] | length' "$frag")
  total_red=$((total_red + cnt))
  kinds=$(jq -r '.redactions // [] | .[].kind' "$frag" | tr '\n' ',' || true)
  seen_kinds="${seen_kinds}${kinds}"
done < "$nd_file"

if (( all_valid == 1 )); then
  ok "every NDJSON line validates against envelope.v1.json"
fi
if (( total_red >= 2 )); then
  ok "stream redactions across all frames: $total_red (>=2 expected)"
else
  fail_ "stream redactions total: $total_red"
fi
if [[ "$seen_kinds" == *"dt0c01"* ]]; then
  ok "stream redactions include kind=dt0c01"
else
  fail_ "stream redactions missing kind=dt0c01"
fi
if [[ "$seen_kinds" == *"gh_token"* ]]; then
  ok "stream redactions include kind=gh_token"
else
  fail_ "stream redactions missing kind=gh_token"
fi

# Terminal frame must also carry the aggregate redactions[] non-empty.
agg_count=$(tail -n 1 "$nd_file" | jq '.redactions | length')
if [[ "$agg_count" -ge 2 ]]; then
  ok "terminal tool_response.redactions has $agg_count entries"
else
  fail_ "terminal tool_response.redactions has $agg_count entries"
fi

# stderr WARNING + audit log must still fire.
if grep -q '^WARNING: redacted secret pattern' "$err_file"; then
  ok "stream stderr WARNING is still emitted"
else
  fail_ "stream stderr WARNING missing"
fi
if [[ -s "$CACHE/last-redaction" ]]; then
  ok "stream audit file appended"
else
  fail_ "stream audit file not appended"
fi

#───────────────────────────────────────────────────────────────────────────────
# Test 3: plain msg (no flag) preserves text-only output, but still redacts
#───────────────────────────────────────────────────────────────────────────────

echo "→ Test 3: plain clawctl msg redacts text output (no envelope)"
plain_file="$TMP/plain.txt"
plain_err="$TMP/plain.err"
set +e
run_clawctl msg ${TEXT_FLAG:+"$TEXT_FLAG"} default "echo my secrets" >"$plain_file" 2>"$plain_err"
ec=$?
set -e

if [[ "$ec" -ne 0 ]]; then
  fail_ "plain msg exited $ec"
fi

if grep -qF "$SECRET_DT" "$plain_file"; then
  fail_ "raw dt0c01 secret leaked into plain msg stdout"
else
  ok "raw dt0c01 secret absent from plain msg stdout"
fi

if jq -e 'type == "object" and has("envelope_version")' "$plain_file" >/dev/null 2>&1; then
  fail_ "plain msg unexpectedly emitted an envelope"
else
  ok "plain msg is not a JSON envelope"
fi

if grep -q '^WARNING: redacted secret pattern' "$plain_err"; then
  ok "plain msg stderr WARNING is still emitted"
else
  fail_ "plain msg stderr WARNING missing"
fi

#───────────────────────────────────────────────────────────────────────────────

echo
if (( fail == 0 )); then
  echo "✓ $pass checks passed"
  exit 0
fi
echo "✗ $fail checks failed ($pass passed)" >&2
exit 1
