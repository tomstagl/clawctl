#!/usr/bin/env bash
# install-resolver.sh — exercise install/install.sh end-to-end without a network.
#
# Strategy: shadow `curl` and `uname` on PATH with fakes that serve from a
# local fixture tree mirroring GitHub's release-asset URL shape:
#
#   <CLAWCTL_DOWNLOAD_BASE>/latest/download/<artifact>
#   <CLAWCTL_DOWNLOAD_BASE>/download/<tag>/<artifact>
#
# The fake curl strips the configured base, looks the remainder up under
# $FIXTURE_DIR, and copies into the requested -o output path. The fake uname
# is driven by FAKE_UNAME_S / FAKE_UNAME_M, so we can table-test every OS/arch
# pair without leaving the developer's machine.
#
# Covered cases:
#   1. darwin/arm64 happy path — artifact installs, checksum verifies, target
#      ends up at the configured CLAWCTL_INSTALL_DIR.
#   2. All four GOOS/GOARCH combos resolve to the right artifact name.
#   3. Unknown OS exits 2.
#   4. Unknown arch exits 2.
#   5. Bad checksum exits 1, leaves no install behind.
#   6. Existing clawctl-like binary at TARGET → upgrade succeeds.
#   7. Existing non-clawctl binary at TARGET → refusal, exit 1, target unchanged.
#
# Exit 0 on success, 1 on any failure.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
INSTALL_SH="$ROOT/install/install.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if [[ ! -x "$INSTALL_SH" ]]; then
  echo "FAIL: $INSTALL_SH not executable" >&2
  exit 1
fi

#───────────────────────────────────────────────────────────────────────────────
# Test scaffolding: fixture directory + fake curl + fake uname + fake sudo.
#───────────────────────────────────────────────────────────────────────────────

FIXTURE_DIR="$TMP/fixtures"
PATH_DIR="$TMP/bin"
mkdir -p "$FIXTURE_DIR" "$PATH_DIR"

# Pick a checksum tool now so we can populate SHA256SUMS the same way the
# release workflow would on Linux runners. macOS lacks `sha256sum` by default
# but ships `shasum`, which produces the same line format.
if command -v sha256sum >/dev/null 2>&1; then
  hash_file() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
  hash_file() { shasum -a 256 "$1" | awk '{print $1}'; }
else
  echo "FAIL: need sha256sum or shasum to build fixtures" >&2
  exit 1
fi

# Build a tiny "clawctl-like" mock binary for each platform. It only needs to
# respond to `version` with a stdout line containing 'clawctl' so the
# post-install heuristic check passes.
write_mock_binary() {
  local path="$1"
  cat >"$path" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
  version|--version) echo "clawctl 0.0.0-test ($(uname -s)/$(uname -m))" ;;
  *) echo "mock clawctl: $*" ;;
esac
EOF
  chmod +x "$path"
}

build_release_fixtures() {
  local kind="$1"   # latest|tagged
  local subdir
  if [[ "$kind" == "latest" ]]; then
    subdir="latest/download"
  else
    subdir="download/v9.9.9"
  fi
  local dest="$FIXTURE_DIR/$subdir"
  mkdir -p "$dest"

  local sums="$dest/SHA256SUMS"
  : >"$sums"
  for art in clawctl-darwin-arm64 clawctl-darwin-amd64 clawctl-linux-amd64 clawctl-linux-arm64; do
    write_mock_binary "$dest/$art"
    printf '%s  %s\n' "$(hash_file "$dest/$art")" "$art" >>"$sums"
  done
}

build_release_fixtures latest
build_release_fixtures tagged

# Fake curl: only honors the flags install.sh actually uses.
cat >"$PATH_DIR/curl" <<EOF
#!/usr/bin/env bash
out=""
url=""
while [ \$# -gt 0 ]; do
  case "\$1" in
    -o|--output) out="\$2"; shift 2 ;;
    --retry)     shift 2 ;;
    -fsSL|-f|-s|-S|-L) shift ;;
    -*)          shift ;;
    *)           url="\$1"; shift ;;
  esac
done
base="${CLAWCTL_DOWNLOAD_BASE_FOR_FAKE:-mock://release}"
case "\$url" in
  "\$base"/*) rel="\${url#\$base/}" ;;
  *) echo "fake-curl: unexpected URL: \$url" >&2; exit 22 ;;
esac
src="$FIXTURE_DIR/\$rel"
if [ ! -f "\$src" ]; then
  echo "fake-curl: no such fixture: \$rel" >&2
  exit 22
fi
if [ -z "\$out" ]; then
  cat "\$src"
else
  cp "\$src" "\$out"
fi
EOF
chmod +x "$PATH_DIR/curl"

# Fake uname driven by FAKE_UNAME_S / FAKE_UNAME_M; defaults to host values.
cat >"$PATH_DIR/uname" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
  -s) echo "${FAKE_UNAME_S:-Darwin}" ;;
  -m) echo "${FAKE_UNAME_M:-arm64}" ;;
  *)  /usr/bin/uname "$@" ;;
esac
EOF
chmod +x "$PATH_DIR/uname"

# Fake sudo: runs the rest of the argv straight through. The resolver should
# never need it because we point CLAWCTL_INSTALL_DIR at a writable temp dir,
# but having a benign stub means an accidental fallback can't hijack the host.
cat >"$PATH_DIR/sudo" <<'EOF'
#!/usr/bin/env bash
echo "fake-sudo: refusing to escalate in tests" >&2
exit 1
EOF
chmod +x "$PATH_DIR/sudo"

run_install() {
  PATH="$PATH_DIR:$PATH" \
  CLAWCTL_DOWNLOAD_BASE="mock://release" \
  CLAWCTL_DOWNLOAD_BASE_FOR_FAKE="mock://release" \
  bash "$INSTALL_SH" "$@"
}

fail=0
pass=0

ok()    { echo "  ok    $*"; pass=$((pass + 1)); }
fail_() { echo "  FAIL  $*" >&2; fail=$((fail + 1)); }

#───────────────────────────────────────────────────────────────────────────────
# Test 1: darwin/arm64 happy path.
#───────────────────────────────────────────────────────────────────────────────

echo "→ Test 1: darwin/arm64 happy path"
INSTALL_DIR="$TMP/case1"
mkdir -p "$INSTALL_DIR"
out=$(FAKE_UNAME_S=Darwin FAKE_UNAME_M=arm64 \
      CLAWCTL_INSTALL_DIR="$INSTALL_DIR" \
      run_install 2>&1) \
  || { fail_ "exit $? (expected 0): $out"; }

if [[ -x "$INSTALL_DIR/clawctl" ]]; then
  ok "binary installed at \$INSTALL_DIR/clawctl"
else
  fail_ "binary missing at $INSTALL_DIR/clawctl"
fi

if [[ "$out" == *"clawctl-darwin-arm64"* ]]; then
  ok "resolver picked clawctl-darwin-arm64"
else
  fail_ "resolver did not log clawctl-darwin-arm64"
  printf '%s\n' "$out" | sed 's/^/      /' >&2
fi

if "$INSTALL_DIR/clawctl" version 2>/dev/null | grep -q clawctl; then
  ok "installed binary responds to \`version\`"
else
  fail_ "installed binary does not respond to \`version\`"
fi

#───────────────────────────────────────────────────────────────────────────────
# Test 2: all four os/arch combos resolve to the right artifact name.
#───────────────────────────────────────────────────────────────────────────────

echo "→ Test 2: os/arch resolution table"
declare -a cases=(
  "Darwin arm64    clawctl-darwin-arm64"
  "Darwin x86_64   clawctl-darwin-amd64"
  "Linux  x86_64   clawctl-linux-amd64"
  "Linux  aarch64  clawctl-linux-arm64"
)
for row in "${cases[@]}"; do
  read -r s m want <<<"$row"
  d="$TMP/case2-$s-$m"
  mkdir -p "$d"
  o=$(FAKE_UNAME_S="$s" FAKE_UNAME_M="$m" \
      CLAWCTL_INSTALL_DIR="$d" \
      run_install 2>&1) \
    || { fail_ "$s/$m: exit $? — $o"; continue; }
  if [[ "$o" == *"$want"* ]] && [[ -x "$d/clawctl" ]]; then
    ok "$s/$m → $want"
  else
    fail_ "$s/$m did not resolve to $want"
    printf '%s\n' "$o" | sed 's/^/      /' >&2
  fi
done

#───────────────────────────────────────────────────────────────────────────────
# Test 3: unknown OS → exit 2.
#───────────────────────────────────────────────────────────────────────────────

echo "→ Test 3: unknown OS exits 2"
d="$TMP/case3"
mkdir -p "$d"
set +e
o=$(FAKE_UNAME_S="Plan9" FAKE_UNAME_M="amd64" \
    CLAWCTL_INSTALL_DIR="$d" \
    run_install 2>&1)
ec=$?
set -e
if [ "$ec" -eq 2 ]; then ok "exit code 2"; else fail_ "exit $ec (expected 2)"; fi
if [[ "$o" == *"unsupported OS"* ]]; then
  ok "stderr names unsupported OS"
else
  fail_ "stderr missing 'unsupported OS': $o"
fi

#───────────────────────────────────────────────────────────────────────────────
# Test 4: unknown arch → exit 2.
#───────────────────────────────────────────────────────────────────────────────

echo "→ Test 4: unknown arch exits 2"
d="$TMP/case4"
mkdir -p "$d"
set +e
o=$(FAKE_UNAME_S="Linux" FAKE_UNAME_M="riscv64" \
    CLAWCTL_INSTALL_DIR="$d" \
    run_install 2>&1)
ec=$?
set -e
if [ "$ec" -eq 2 ]; then ok "exit code 2"; else fail_ "exit $ec (expected 2)"; fi
if [[ "$o" == *"unsupported architecture"* ]]; then
  ok "stderr names unsupported architecture"
else
  fail_ "stderr missing 'unsupported architecture': $o"
fi

#───────────────────────────────────────────────────────────────────────────────
# Test 5: bad checksum → exit 1, no install.
#───────────────────────────────────────────────────────────────────────────────

echo "→ Test 5: bad checksum exits 1"
# Corrupt the SHA256SUMS for the latest fixture, then run.
cp "$FIXTURE_DIR/latest/download/SHA256SUMS" "$TMP/SHA256SUMS.good"
sed 's/^[0-9a-f]/0/' "$TMP/SHA256SUMS.good" >"$FIXTURE_DIR/latest/download/SHA256SUMS"
d="$TMP/case5"
mkdir -p "$d"
set +e
o=$(FAKE_UNAME_S=Darwin FAKE_UNAME_M=arm64 \
    CLAWCTL_INSTALL_DIR="$d" \
    run_install 2>&1)
ec=$?
set -e
# Restore so later tests see clean fixtures.
cp "$TMP/SHA256SUMS.good" "$FIXTURE_DIR/latest/download/SHA256SUMS"

if [ "$ec" -eq 1 ]; then ok "exit code 1"; else fail_ "exit $ec (expected 1)"; fi
if [[ "$o" == *"checksum"* ]]; then
  ok "stderr names checksum failure"
else
  fail_ "stderr missing 'checksum': $o"
fi
if [[ ! -e "$d/clawctl" ]]; then
  ok "no binary written on checksum failure"
else
  fail_ "binary leaked into $d/clawctl despite checksum failure"
fi

#───────────────────────────────────────────────────────────────────────────────
# Test 6: existing clawctl-like binary → upgrade succeeds.
#───────────────────────────────────────────────────────────────────────────────

echo "→ Test 6: upgrade over an existing clawctl"
d="$TMP/case6"
mkdir -p "$d"
write_mock_binary "$d/clawctl"
set +e
o=$(FAKE_UNAME_S=Darwin FAKE_UNAME_M=arm64 \
    CLAWCTL_INSTALL_DIR="$d" \
    run_install 2>&1)
ec=$?
set -e
if [ "$ec" -eq 0 ]; then
  ok "upgrade exits 0"
else
  fail_ "upgrade exit $ec — $o"
fi
if [[ "$o" == *"upgrading"* ]] || [[ "$o" == *"installed"* ]]; then
  ok "upgrade message visible"
else
  fail_ "upgrade message missing: $o"
fi

#───────────────────────────────────────────────────────────────────────────────
# Test 7: existing non-clawctl binary → refuse, exit 1, file untouched.
#───────────────────────────────────────────────────────────────────────────────

echo "→ Test 7: refuse to overwrite a non-clawctl binary"
d="$TMP/case7"
mkdir -p "$d"
cat >"$d/clawctl" <<'EOF'
#!/usr/bin/env bash
# Stand-in for a foreign binary at /usr/local/bin/clawctl — e.g. an alias to
# the OpenShift `oc` client or kubectl. Output deliberately does NOT contain
# the literal string the resolver heuristic looks for.
echo "Client Version: v1.28.0+platform"
exit 0
EOF
chmod +x "$d/clawctl"
sentinel_before=$(hash_file "$d/clawctl")
set +e
o=$(FAKE_UNAME_S=Darwin FAKE_UNAME_M=arm64 \
    CLAWCTL_INSTALL_DIR="$d" \
    run_install 2>&1)
ec=$?
set -e
sentinel_after=$(hash_file "$d/clawctl")
if [ "$ec" -eq 1 ]; then
  ok "refusal exits 1"
else
  fail_ "refusal exit $ec (expected 1) — $o"
fi
if [[ "$o" == *"refusing to overwrite"* ]]; then
  ok "stderr names refusal"
else
  fail_ "stderr missing 'refusing to overwrite': $o"
fi
if [[ "$sentinel_before" == "$sentinel_after" ]]; then
  ok "pre-existing binary left untouched"
else
  fail_ "pre-existing binary was modified"
fi

#───────────────────────────────────────────────────────────────────────────────

echo
if (( fail == 0 )); then
  echo "✓ $pass checks passed"
  exit 0
fi
echo "✗ $fail checks failed ($pass passed)" >&2
exit 1
