#!/bin/sh
# Skill-local gate: sanitize -> compile -> tests -> dual-arch E2E -> diff check.
set -eu
cd "$(dirname "$0")/.."

python3 tests/sanitize.py
python3 -m compileall -q tests

python3 -m unittest discover -s tests -v
python3 tests/e2e_local.py

if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  git diff --check
fi
echo "CI PASS"
