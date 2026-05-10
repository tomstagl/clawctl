#!/usr/bin/env bash
# parity-trace.sh — diff `clawctl trace` between the bash entrypoint and the
# Go binary against a recorded Jaeger response fixture.
#
# Coverage:
#   1. happy path (3 spans, two services) → byte-identical stdout, exit 0
#   2. Jaeger errors[] populated         → byte-identical stdout, exit 0
#   3. empty data[] (no-spans)           → byte-identical stdout, exit 0
#   4. 30-span limit (35 spans served)   → exact 30-row block, exit 0
#   5. Jaeger unreachable                → header still printed, exit 0
#   6. CLAWCTL_JAEGER_UI unset           → exit 2 (usage)
#   7. missing trace-id arg              → exit 2 (usage)
#
# Strategy: spin up a tiny Python HTTP server pinned to 127.0.0.1:<random>
# that serves a fixture body chosen per case via $TMP/mode. Both binaries
# then point at the mock via CLAWCTL_JAEGER_UI and we diff stdout/stderr/exit.
# Note: bash trace shells out to `python3 -c '...'` for span formatting; we
# require python3 for both the mock server *and* the bash code path.
#
# Exit 0 on success, 1 on any failure.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BASH_BIN="$ROOT/clawctl"
TMP="$(mktemp -d)"
GO_BIN="$TMP/clawctl-go"
SERVER_PID=""
cleanup() {
  if [[ -n "$SERVER_PID" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
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

if ! command -v python3 >/dev/null 2>&1; then
  echo "FAIL: python3 not on PATH (bash 'clawctl trace' requires it)" >&2
  exit 1
fi

echo "building Go binary → $GO_BIN"
( cd "$ROOT" && CGO_ENABLED=0 go build -o "$GO_BIN" ./cmd/clawctl )

# ─── Fixture bodies ──────────────────────────────────────────────────────
mkdir -p "$TMP/fixtures"
cat >"$TMP/fixtures/happy.json" <<'JSON'
{"data":[{"spans":[
  {"operationName":"GET /v1/chat/completions","duration":1500000,"processID":"p1"},
  {"operationName":"agent.run","duration":850000,"processID":"p2"},
  {"operationName":"redis.get","duration":1200,"processID":"p1"}
],"processes":{
  "p1":{"serviceName":"openclaw-gateway"},
  "p2":{"serviceName":"agent-runner"}
}}]}
JSON

cat >"$TMP/fixtures/errors.json" <<'JSON'
{"errors":[{"msg":"trace not found"}]}
JSON

cat >"$TMP/fixtures/no-spans.json" <<'JSON'
{"data":[]}
JSON

# 35-span fixture exercises the 30-row limit. Generate it in Python so the
# JSON is exact and we don't fight bash quoting.
python3 - "$TMP/fixtures/many.json" <<'PY'
import json, sys
spans = [{"operationName":"op","duration":1000,"processID":"p"} for _ in range(35)]
doc = {"data":[{"spans":spans,"processes":{"p":{"serviceName":"svc"}}}]}
with open(sys.argv[1],"w") as f: json.dump(doc,f)
PY

# ─── Mock Jaeger server ──────────────────────────────────────────────────
# Mode is read fresh per request from $TMP/mode; cases swap fixture filenames
# without restarting the server. The server only answers /jaeger/api/traces/<id>;
# anything else 404s.
PORT_FILE="$TMP/port"
echo "happy" > "$TMP/mode"
SERVER_PY="$TMP/server.py"
cat >"$SERVER_PY" <<'PY'
import os, sys, socketserver
from http.server import BaseHTTPRequestHandler, HTTPServer

port_file = sys.argv[1]
mode_file = sys.argv[2]
fixture_dir = sys.argv[3]

def fixture():
    try:
        with open(mode_file) as f:
            return f.read().strip()
    except FileNotFoundError:
        return "happy"

class H(BaseHTTPRequestHandler):
    def do_GET(self):
        # /jaeger/api/traces/<id> only; 404 otherwise.
        if not self.path.startswith("/jaeger/api/traces/"):
            self.send_response(404); self.end_headers(); return
        path = os.path.join(fixture_dir, fixture() + ".json")
        if not os.path.exists(path):
            self.send_response(404); self.end_headers(); return
        with open(path, "rb") as f: body = f.read()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *a, **kw): pass

class HS(HTTPServer):
    # Skip socket.getfqdn() to avoid reverse-DNS hangs in sandboxed CI.
    def server_bind(self):
        socketserver.TCPServer.server_bind(self)
        _, port = self.socket.getsockname()[:2]
        self.server_name = "localhost"
        self.server_port = port

srv = HS(("127.0.0.1", 0), H)
with open(port_file, "w") as f:
    f.write(str(srv.server_address[1]))
srv.serve_forever()
PY

python3 -u "$SERVER_PY" "$PORT_FILE" "$TMP/mode" "$TMP/fixtures" </dev/null >"$TMP/server.log" 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 40); do
  if [[ -s "$PORT_FILE" ]]; then break; fi
  sleep 0.05
done
if [[ ! -s "$PORT_FILE" ]]; then
  echo "FAIL: mock Jaeger server didn't start" >&2
  exit 1
fi
PORT="$(cat "$PORT_FILE")"
JAEGER="http://127.0.0.1:$PORT"

# Pre-pick a guaranteed-refused port for the unreachable case.
REFUSED_PORT="$(python3 -c '
import socket
s = socket.socket(); s.bind(("127.0.0.1", 0))
print(s.getsockname()[1]); s.close()
')"
REFUSED_HOST="http://127.0.0.1:$REFUSED_PORT"

# ─── Test plumbing ───────────────────────────────────────────────────────
fail=0; pass=0
ok()   { echo "  ok    $*"; pass=$((pass + 1)); }
nope() { echo "  FAIL  $*" >&2; fail=$((fail + 1)); }

run_one() {
  # $1 = label, $2 = bin, rest = args; sets $exit, $out, $err
  local _label="$1" _bin="$2"
  local _out_file="$TMP/$_label.out" _err_file="$TMP/$_label.err"
  set +e
  "$_bin" "${@:3}" >"$_out_file" 2>"$_err_file"
  exit_code=$?
  set -e
  out="$(cat "$_out_file")"
  err="$(cat "$_err_file")"
  exit="$exit_code"
}

diff_pair() {
  local _label="$1" _bash_out="$2" _bash_err="$3" _bash_exit="$4"
  local _go_out="$5" _go_err="$6" _go_exit="$7"
  if [[ "$_bash_out" == "$_go_out" ]]; then
    ok "$_label: stdout matches"
  else
    nope "$_label: stdout diverges"
    diff <(printf '%s' "$_bash_out") <(printf '%s' "$_go_out") || true
  fi
  if [[ "$_bash_err" == "$_go_err" ]]; then
    ok "$_label: stderr matches"
  else
    nope "$_label: stderr diverges"
    diff <(printf '%s' "$_bash_err") <(printf '%s' "$_go_err") || true
  fi
  if [[ "$_bash_exit" == "$_go_exit" ]]; then
    ok "$_label: exit $_bash_exit matches"
  else
    nope "$_label: exit diverges (bash=$_bash_exit, go=$_go_exit)"
  fi
}

parity_case() {
  local _label="$1"; shift
  run_one "bash-$_label" "$BASH_BIN" "$@" || true
  local b_out="$out" b_err="$err" b_exit="$exit"
  run_one "go-$_label"   "$GO_BIN"   "$@" || true
  local g_out="$out" g_err="$err" g_exit="$exit"
  diff_pair "$_label" "$b_out" "$b_err" "$b_exit" "$g_out" "$g_err" "$g_exit"
}

TID="0000000000000000aaaaaaaaaaaaaaaa"

# ─── case 1: happy path ──────────────────────────────────────────────────
echo "case 1: happy (3 spans, 2 services)"
echo "happy" > "$TMP/mode"
export CLAWCTL_JAEGER_UI="$JAEGER"
parity_case "happy" trace "$TID"

# ─── case 2: Jaeger errors[] populated ───────────────────────────────────
echo "case 2: Jaeger errors[] populated"
echo "errors" > "$TMP/mode"
parity_case "errors" trace "$TID"

# ─── case 3: empty data[] (no spans) ─────────────────────────────────────
echo "case 3: empty data[] (no spans)"
echo "no-spans" > "$TMP/mode"
parity_case "no-spans" trace "$TID"

# ─── case 4: 35 spans → 30-row limit ─────────────────────────────────────
echo "case 4: 35 spans → 30-row limit"
echo "many" > "$TMP/mode"
parity_case "many" trace "$TID"

# ─── case 5: unreachable Jaeger ──────────────────────────────────────────
echo "case 5: unreachable Jaeger (refused port) → header only, exit 0"
CLAWCTL_JAEGER_UI="$REFUSED_HOST" parity_case "refused" trace "$TID"
unset CLAWCTL_JAEGER_UI

# ─── case 6: CLAWCTL_JAEGER_UI unset ─────────────────────────────────────
echo "case 6: CLAWCTL_JAEGER_UI unset → exit 2"
parity_case "no-jaeger" trace "$TID"

# ─── case 7: missing trace-id arg ────────────────────────────────────────
echo "case 7: missing trace-id arg → exit 2"
export CLAWCTL_JAEGER_UI="$JAEGER"
parity_case "no-arg" trace
unset CLAWCTL_JAEGER_UI

#───────────────────────────────────────────────────────────────────────────
echo
echo "passed: $pass    failed: $fail"
[[ "$fail" -eq 0 ]] || exit 1
