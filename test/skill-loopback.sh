#!/usr/bin/env bash
# Validates skills/openclaw-loopback/SKILL.md contract invariants:
#   - R-1..R-12 rule headings are all present
#   - YAML deliverable header block parses as valid YAML
#   - Label definitions appear in a single canonical block
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKILL="$ROOT/skills/openclaw-loopback/SKILL.md"
PASS=0
FAIL=0

ok()   { echo "  ok: $*"; PASS=$((PASS + 1)); }
fail() { echo "FAIL: $*"; FAIL=$((FAIL + 1)); }

echo "=== skill-loopback.sh ==="
echo ""

if [ ! -f "$SKILL" ]; then
  echo "FAIL: $SKILL not found"
  exit 1
fi

# --- R-1..R-12 rule headings ---
echo "--- Rule headings ---"
for n in $(seq 1 12); do
  if grep -qE "^### R-${n} " "$SKILL"; then
    ok "R-${n} heading present"
  else
    fail "R-${n} heading missing in $SKILL"
  fi
done

# --- YAML deliverable header ---
echo ""
echo "--- YAML deliverable header ---"
yaml_block=$(awk '/^```yaml$/{f=1;next} f && /^```$/{exit} f{print}' "$SKILL")
if [ -z "$yaml_block" ]; then
  fail "No fenced yaml block found in $SKILL"
else
  # Require all six mandatory keys to be present
  all_keys=1
  for key in agent run-id traceparent started ended status; do
    if ! echo "$yaml_block" | grep -q "^${key}:"; then
      fail "YAML deliverable header missing key '${key}' in $SKILL"
      all_keys=0
    fi
  done
  if [ "$all_keys" -eq 1 ]; then
    ok "YAML deliverable header contains all required keys"
  fi
  # Validate YAML syntax with python3 yaml.safe_load if available, else yq, else skip
  if python3 -c "import yaml" 2>/dev/null; then
    if echo "$yaml_block" | python3 -c "
import sys, yaml
try:
    yaml.safe_load(sys.stdin.read())
    print('ok')
except Exception as e:
    print('error:', e)
    sys.exit(1)
" 2>/dev/null | grep -q '^ok$'; then
      ok "YAML deliverable header parses with python3 yaml.safe_load"
    else
      fail "YAML deliverable header failed yaml.safe_load in $SKILL"
    fi
  elif command -v yq >/dev/null 2>&1; then
    if echo "$yaml_block" | yq . >/dev/null 2>&1; then
      ok "YAML deliverable header parses with yq"
    else
      fail "YAML deliverable header failed yq parse in $SKILL"
    fi
  else
    echo " skip: yaml.safe_load and yq unavailable; key-presence check above is the fallback"
  fi
fi

# --- Label canonical block ---
echo ""
echo "--- Label canonical block ---"
# Match only bullet-point label definitions, not prose references
label_lines=$(grep -n "^- \`openclaw" "$SKILL" 2>/dev/null | awk -F: '{print $1}' || true)
if [ -z "$label_lines" ]; then
  fail "No bullet-point label definitions found (expected lines starting with '- \`openclaw') in $SKILL"
else
  line_count=$(echo "$label_lines" | wc -l | tr -d ' ')
  first_line=$(echo "$label_lines" | head -1)
  last_line=$(echo "$label_lines" | tail -1)
  spread=$((last_line - first_line))
  if [ "$spread" -le 10 ]; then
    ok "Label definitions in single canonical block ($line_count defs, lines $first_line-$last_line)"
  else
    fail "Label definitions scattered in $SKILL (lines $first_line-$last_line, spread=$spread > 10)"
  fi
fi

echo ""
echo "Results: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ] || exit 1
