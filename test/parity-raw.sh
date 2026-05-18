#!/usr/bin/env bash
# parity-raw.sh — diff `clawctl raw` between the bash entrypoint and the Go
# binary against a controlled mock gateway.
#
# Coverage:
#   1. GET → byte-identical body on stdout, exit 0, traceparent reaches the
#      server in W3C shape, trace-id printed to stderr by both
#   2. POST with -d → both forward the body, exit 0, server sees the same
#      method/body/headers
#   3. HTTP 500 error → both exit 22 and forward the body to stdout
#   4. CLAWCTL_HOST unset → both exit 2
#
# Stdout body comparison is byte-exact for the success paths. stderr is NOT
# byte-compared because the trace-id is generated per call and so the lines
# differ; we only assert that both binaries emitted *some* trace-id line.
#
# Like parity-models.sh, this test stashes a one-shot token in the macOS
# Keychain because the bash entrypoint forbids env/disk fallbacks (design
# principle #2). On non-macOS hosts (no `security` tool) the test skips.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BASH_BIN="$ROOT/clawctl.bash"
TMP="$(mktemp -d)"
GO_BIN="$TMP/clawctl-go"
SERVER_PID=""
KEYCHAIN_SVC="clawctl-parity-raw-$$"
KEYCHAIN_ADDED=0

cleanup() {
  if [[ -n "$SERVER_PID" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
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

if ! command -v python3 >/dev/null 2>&1; then
  echo "FAIL: python3 not on PATH" >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "FAIL: jq not on PATH" >&2
  exit 1
fi

if ! command -v security >/dev/null 2>&1; then
  echo "SKIP: macOS \`security\` tool not available — bash binary cannot read a token" >&2
  exit 0
fi

echo "building Go binary → $GO_BIN"
( cd "$ROOT" && CGO_ENABLED=0 go build -o "$GO_BIN" ./cmd/clawctl )

if security add-generic-password -s "$KEYCHAIN_SVC" -a "$USER" -w "tok-parity-raw" >/dev/null 2>&1; then
  KEYCHAIN_ADDED=1
else
  echo "FAIL: could not add temporary keychain item" >&2
  exit 1
fi

# Mock gateway. Records the last-seen request to $TMP/last-req.json
# (method, path, body, traceparent header) so we can assert what reached the
# wire.
PORT_FILE="$TMP/port"
echo "ok" > "$TMP/mode"
SERVER_PY="$TMP/server.py"
cat >"$SERVER_PY" <<'PY'
import json
import sys
import socketserver
from http.server import BaseHTTPRequestHandler, HTTPServer

port_file = sys.argv[1]
mode_file = sys.argv[2]
last_req_file = sys.argv[3]

def mode():
    try:
        with open(mode_file) as f:
            return f.read().strip()
    except FileNotFoundError:
        return "ok"

def record(handler, body):
    rec = {
        "method": handler.command,
        "path": handler.path,
        "body": body.decode("utf-8", "replace"),
        "traceparent": handler.headers.get("traceparent", ""),
        "authorization": handler.headers.get("Authorization", ""),
    }
    with open(last_req_file, "w") as f:
        json.dump(rec, f)

class H(BaseHTTPRequestHandler):
    def _read_body(self):
        n = int(self.headers.get("Content-Length") or 0)
        return self.rfile.read(n) if n > 0 else b""

    def _serve(self):
        body = self._read_body()
        record(self, body)
        m = mode()
        if m == "ok":
            payload = b'{"echoed":"ok"}'
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
        elif m == "500":
            payload = b'{"error":"server_busted"}'
            self.send_response(500)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
        else:
            self.send_response(500); self.end_headers()

    def do_GET(self): self._serve()
    def do_POST(self): self._serve()

    def log_message(self, *args, **kwargs):
        pass

class HS(HTTPServer):
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

LAST_REQ="$TMP/last-req.json"
python3 -u "$SERVER_PY" "$PORT_FILE" "$TMP/mode" "$LAST_REQ" </dev/null >"$TMP/server.log" 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 40); do
  if [[ -s "$PORT_FILE" ]]; then break; fi
  sleep 0.05
done
if [[ ! -s "$PORT_FILE" ]]; then
  echo "FAIL: mock server didn't start" >&2
  exit 1
fi
PORT="$(cat "$PORT_FILE")"
HOST="http://127.0.0.1:$PORT"

fail=0; pass=0
ok()   { echo "  ok    $*"; pass=$((pass + 1)); }
nope() { echo "  FAIL  $*" >&2; fail=$((fail + 1)); }

run_one() {
  # $1 = label, $2 = bin, rest = args; sets $exit_code, $out, $err
  local _label="$1" _bin="$2"
  local _out_file="$TMP/$_label.out" _err_file="$TMP/$_label.err"
  set +e
  "$_bin" "${@:3}" >"$_out_file" 2>"$_err_file"
  exit_code=$?
  set -e
  out="$(cat "$_out_file")"
  err="$(cat "$_err_file")"
}

#───────────────────────────────────────────────────────────────────────────
echo "case 1: GET → byte-identical body on stdout, both exit 0, traceparent reaches server"
echo "ok" > "$TMP/mode"

CLAWCTL_HOST="$HOST" CLAWCTL_TIMEOUT=5 \
  CLAWCTL_KEYCHAIN_SERVICE="$KEYCHAIN_SVC" \
  run_one "bash-get" "$BASH_BIN" raw GET /v1/anything
bash_out="$out"; bash_err="$err"; bash_exit="$exit_code"
bash_tp="$(jq -r '.traceparent' "$LAST_REQ")"
bash_method="$(jq -r '.method' "$LAST_REQ")"

CLAWCTL_HOST="$HOST" CLAWCTL_TIMEOUT=5 \
  CLAWCTL_KEYCHAIN_SERVICE="$KEYCHAIN_SVC" \
  run_one "go-get" "$GO_BIN" raw GET /v1/anything
go_out="$out"; go_err="$err"; go_exit="$exit_code"
go_tp="$(jq -r '.traceparent' "$LAST_REQ")"
go_method="$(jq -r '.method' "$LAST_REQ")"

[[ "$bash_exit" -eq 0 ]] && ok "bash exit 0 on GET" || nope "bash exit $bash_exit"
[[ "$go_exit"   -eq 0 ]] && ok "go   exit 0 on GET" || nope "go   exit $go_exit"
[[ "$bash_method" == "GET" ]] && ok "bash sent GET" || nope "bash method=$bash_method"
[[ "$go_method"   == "GET" ]] && ok "go   sent GET" || nope "go   method=$go_method"

if [[ "$bash_out" == "$go_out" ]]; then ok "GET stdout byte-identical"
else
  nope "GET stdout diverged"
  diff <(printf '%s' "$bash_out") <(printf '%s' "$go_out") || true
fi
echo "$bash_out" | jq -e '.echoed == "ok"' >/dev/null \
  && ok "GET body parses as expected JSON" \
  || nope "GET body not the expected JSON"

# traceparent shape: 00-<32hex>-<16hex>-01 (W3C version-00 sampled)
if [[ "$bash_tp" =~ ^00-[0-9a-f]{32}-[0-9a-f]{16}-01$ ]]; then
  ok "bash sent W3C traceparent ($bash_tp)"
else
  nope "bash traceparent malformed: $bash_tp"
fi
if [[ "$go_tp" =~ ^00-[0-9a-f]{32}-[0-9a-f]{16}-01$ ]]; then
  ok "go   sent W3C traceparent ($go_tp)"
else
  nope "go traceparent malformed: $go_tp"
fi

# Both should print "trace-id: <hex>" to stderr.
echo "$bash_err" | grep -Eq 'trace-id: [0-9a-f]{32}' \
  && ok "bash stderr carries trace-id line" \
  || nope "bash stderr missing trace-id: $bash_err"
echo "$go_err" | grep -Eq 'trace-id: [0-9a-f]{32}' \
  && ok "go   stderr carries trace-id line" \
  || nope "go stderr missing trace-id: $go_err"

#───────────────────────────────────────────────────────────────────────────
echo "case 2: POST with -d → both forward body, exit 0, server sees the body"
echo "ok" > "$TMP/mode"

CLAWCTL_HOST="$HOST" CLAWCTL_TIMEOUT=5 \
  CLAWCTL_KEYCHAIN_SERVICE="$KEYCHAIN_SVC" \
  run_one "bash-post" "$BASH_BIN" raw POST /v1/echo \
    -H 'Content-Type: application/json' -d '{"x":1}'
bash_out="$out"; bash_exit="$exit_code"
bash_body="$(jq -r '.body' "$LAST_REQ")"
bash_method="$(jq -r '.method' "$LAST_REQ")"

CLAWCTL_HOST="$HOST" CLAWCTL_TIMEOUT=5 \
  CLAWCTL_KEYCHAIN_SERVICE="$KEYCHAIN_SVC" \
  run_one "go-post" "$GO_BIN" raw POST /v1/echo \
    -H 'Content-Type: application/json' -d '{"x":1}'
go_out="$out"; go_exit="$exit_code"
go_body="$(jq -r '.body' "$LAST_REQ")"
go_method="$(jq -r '.method' "$LAST_REQ")"

[[ "$bash_exit" -eq 0 ]] && ok "bash exit 0 on POST" || nope "bash exit $bash_exit"
[[ "$go_exit"   -eq 0 ]] && ok "go   exit 0 on POST" || nope "go   exit $go_exit"
[[ "$bash_method" == "POST" ]] && ok "bash sent POST" || nope "bash method=$bash_method"
[[ "$go_method"   == "POST" ]] && ok "go   sent POST" || nope "go   method=$go_method"
[[ "$bash_body" == '{"x":1}' ]] && ok "bash forwarded body" || nope "bash body=$bash_body"
[[ "$go_body"   == '{"x":1}' ]] && ok "go   forwarded body" || nope "go   body=$go_body"
if [[ "$bash_out" == "$go_out" ]]; then ok "POST stdout byte-identical"
else
  nope "POST stdout diverged"
  diff <(printf '%s' "$bash_out") <(printf '%s' "$go_out") || true
fi

#───────────────────────────────────────────────────────────────────────────
echo "case 3: HTTP 500 → both exit 22 and forward body on stdout"
echo "500" > "$TMP/mode"

CLAWCTL_HOST="$HOST" CLAWCTL_TIMEOUT=5 \
  CLAWCTL_KEYCHAIN_SERVICE="$KEYCHAIN_SVC" \
  run_one "bash-500" "$BASH_BIN" raw GET /v1/boom || true
bash_out="$out"; bash_exit="$exit_code"

CLAWCTL_HOST="$HOST" CLAWCTL_TIMEOUT=5 \
  CLAWCTL_KEYCHAIN_SERVICE="$KEYCHAIN_SVC" \
  run_one "go-500" "$GO_BIN" raw GET /v1/boom || true
go_out="$out"; go_exit="$exit_code"

[[ "$bash_exit" -eq 22 ]] && ok "bash exit 22 on HTTP 500" || nope "bash exit $bash_exit (want 22)"
[[ "$go_exit"   -eq 22 ]] && ok "go   exit 22 on HTTP 500" || nope "go   exit $go_exit (want 22)"
echo "$bash_out" | grep -q 'server_busted' \
  && ok "bash forwarded error body to stdout" \
  || nope "bash stdout missing error body: $bash_out"
echo "$go_out" | grep -q 'server_busted' \
  && ok "go   forwarded error body to stdout" \
  || nope "go stdout missing error body: $go_out"

#───────────────────────────────────────────────────────────────────────────
echo "case 4: CLAWCTL_HOST unset → both exit 2"
unset CLAWCTL_HOST
run_one "bash-nohost" "$BASH_BIN" raw GET /any || true
bash_exit="$exit_code"
run_one "go-nohost"   "$GO_BIN"   raw GET /any || true
go_exit="$exit_code"
[[ "$bash_exit" -eq 2 ]] && ok "bash exit 2 when CLAWCTL_HOST unset" || nope "bash exit $bash_exit (want 2)"
[[ "$go_exit"   -eq 2 ]] && ok "go   exit 2 when CLAWCTL_HOST unset" || nope "go   exit $go_exit (want 2)"

#───────────────────────────────────────────────────────────────────────────
echo
echo "passed: $pass    failed: $fail"
[[ "$fail" -eq 0 ]] || exit 1
