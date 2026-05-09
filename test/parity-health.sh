#!/usr/bin/env bash
# parity-health.sh — diff `clawctl health` between the bash entrypoint and
# the Go binary against a controlled mock gateway.
#
# Coverage:
#   1. 200 → identical pretty-printed JSON on stdout, empty stderr, exit 0
#   2. 500 → exit 22 from both
#   3. server unreachable (bound-then-closed port) → exit 7 from both
#   4. CLAWCTL_HOST unset → exit 2 from both
#
# Cases 2/3 are exit-only because the bash path lets curl emit its own
# stderr ("curl: (22) The requested URL returned error: 500", etc.) while
# the Go path emits a clawctl-branded message. Stdout on the 5xx path also
# diverges because curl --fail-with-body forces curl=22 onto a pipefail-set
# `| jq`, and we don't try to mimic that pipeline byte-for-byte.
#
# Strategy: spin up a tiny Python HTTP server with controllable behavior
# pinned to 127.0.0.1:<random>. Build the Go binary into a temp path so it
# doesn't clobber the bash entrypoint at the repo root.

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
  echo "FAIL: python3 not on PATH" >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "FAIL: jq not on PATH" >&2
  exit 1
fi

echo "building Go binary → $GO_BIN"
( cd "$ROOT" && CGO_ENABLED=0 go build -o "$GO_BIN" ./cmd/clawctl )

# Start a controllable mock gateway. Mode is read fresh per request from
# $TMP/mode so cases can switch behavior without restarting the server.
PORT_FILE="$TMP/port"
echo "ok" > "$TMP/mode"
SERVER_PY="$TMP/server.py"
cat >"$SERVER_PY" <<'PY'
import sys
import socketserver
from http.server import BaseHTTPRequestHandler, HTTPServer

port_file = sys.argv[1]
mode_file = sys.argv[2]

def mode():
    try:
        with open(mode_file) as f:
            return f.read().strip()
    except FileNotFoundError:
        return "ok"

class H(BaseHTTPRequestHandler):
    def do_GET(self):
        m = mode()
        if self.path != "/health":
            self.send_response(404); self.end_headers(); return
        if m == "ok":
            body = b'{"status":"ok"}'
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        elif m == "500":
            body = b'{"error":"server_busted"}'
            self.send_response(500)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        else:
            self.send_response(500); self.end_headers()
    def log_message(self, *args, **kwargs):
        pass

class HS(HTTPServer):
    # Override server_bind to skip socket.getfqdn() — reverse DNS lookup
    # hangs in sandboxed CI environments.
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

python3 -u "$SERVER_PY" "$PORT_FILE" "$TMP/mode" </dev/null >"$TMP/server.log" 2>&1 &
SERVER_PID=$!

# Wait up to 2s for the server to write its bound port.
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

# Pre-pick a guaranteed-refused port: bind, capture, release.
REFUSED_PORT="$(python3 -c '
import socket
s = socket.socket(); s.bind(("127.0.0.1", 0))
print(s.getsockname()[1]); s.close()
')"
REFUSED_HOST="http://127.0.0.1:$REFUSED_PORT"

fail=0; pass=0
ok()   { echo "  ok    $*"; pass=$((pass + 1)); }
nope() { echo "  FAIL  $*" >&2; fail=$((fail + 1)); }

run_one() {
  # $1 = label, $2 = bin, rest = args; sets $exit, $out, $err
  local _bin="$2"
  out_file="$TMP/$1.out"; err_file="$TMP/$1.err"
  set +e
  "$_bin" "${@:3}" >"$out_file" 2>"$err_file"
  exit_code=$?
  set -e
  out="$(cat "$out_file")"
  err="$(cat "$err_file")"
  exit="$exit_code"
}

#───────────────────────────────────────────────────────────────────────────
echo "case 1: 200 OK → byte-identical stdout, empty stderr, exit 0"
echo "ok" > "$TMP/mode"
CLAWCTL_HOST="$HOST" CLAWCTL_TIMEOUT=5 run_one "bash-ok"  "$BASH_BIN" health
bash_out="$out"; bash_err="$err"; bash_exit="$exit"
CLAWCTL_HOST="$HOST" CLAWCTL_TIMEOUT=5 run_one "go-ok"    "$GO_BIN"   health
go_out="$out"; go_err="$err"; go_exit="$exit"

[[ "$bash_exit" -eq 0 ]] && ok "bash exit 0" || nope "bash exit $bash_exit"
[[ "$go_exit"   -eq 0 ]] && ok "go   exit 0" || nope "go   exit $go_exit"
if [[ "$bash_out" == "$go_out" ]]; then ok "stdout matches"
else
  nope "stdout diverges"
  diff <(printf '%s' "$bash_out") <(printf '%s' "$go_out") || true
fi
[[ -z "$bash_err" ]] && ok "bash stderr empty" || nope "bash stderr: $bash_err"
[[ -z "$go_err"   ]] && ok "go   stderr empty" || nope "go   stderr: $go_err"
echo "$bash_out" | jq -e '.status == "ok"' >/dev/null && ok "bash stdout is valid JSON with status=ok" || nope "bash stdout not the expected JSON"

#───────────────────────────────────────────────────────────────────────────
echo "case 2: 500 → exit 22 from both"
echo "500" > "$TMP/mode"
CLAWCTL_HOST="$HOST" CLAWCTL_TIMEOUT=5 run_one "bash-500" "$BASH_BIN" health || true
bash_exit="$exit"
CLAWCTL_HOST="$HOST" CLAWCTL_TIMEOUT=5 run_one "go-500"   "$GO_BIN"   health || true
go_exit="$exit"
[[ "$bash_exit" -eq 22 ]] && ok "bash exit 22 on HTTP 500" || nope "bash exit $bash_exit (want 22)"
[[ "$go_exit"   -eq 22 ]] && ok "go   exit 22 on HTTP 500" || nope "go   exit $go_exit (want 22)"

#───────────────────────────────────────────────────────────────────────────
echo "case 3: connection refused → exit 7 from both"
CLAWCTL_HOST="$REFUSED_HOST" CLAWCTL_TIMEOUT=2 run_one "bash-refused" "$BASH_BIN" health || true
bash_exit="$exit"
CLAWCTL_HOST="$REFUSED_HOST" CLAWCTL_TIMEOUT=2 run_one "go-refused"   "$GO_BIN"   health || true
go_exit="$exit"
[[ "$bash_exit" -eq 7 ]] && ok "bash exit 7 on refused" || nope "bash exit $bash_exit (want 7)"
[[ "$go_exit"   -eq 7 ]] && ok "go   exit 7 on refused" || nope "go   exit $go_exit (want 7)"

#───────────────────────────────────────────────────────────────────────────
echo "case 4: CLAWCTL_HOST unset → exit 2 from both"
unset CLAWCTL_HOST
run_one "bash-nohost" "$BASH_BIN" health || true
bash_exit="$exit"
run_one "go-nohost"   "$GO_BIN"   health || true
go_exit="$exit"
[[ "$bash_exit" -eq 2 ]] && ok "bash exit 2 when CLAWCTL_HOST unset" || nope "bash exit $bash_exit (want 2)"
[[ "$go_exit"   -eq 2 ]] && ok "go   exit 2 when CLAWCTL_HOST unset" || nope "go   exit $go_exit (want 2)"

#───────────────────────────────────────────────────────────────────────────
echo
echo "passed: $pass    failed: $fail"
[[ "$fail" -eq 0 ]] || exit 1
