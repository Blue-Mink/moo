#!/bin/sh
# Skill-local gate: sanitize -> compile -> tests -> dual-arch E2E -> diff check.
set -eu
cd "$(dirname "$0")/.."

python3 tests/sanitize.py
python3 -m compileall -q tests

python3 -m unittest discover -s tests -v

# update_moo_json.py 维护工具测试套件（harness / extra / 变异回归）
python3 tests/moojson_harness.py
python3 tests/moojson_extra.py
python3 tests/moojson_rev.py

python3 tests/e2e_local.py

if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  git diff --check
fi
echo "CI PASS"
