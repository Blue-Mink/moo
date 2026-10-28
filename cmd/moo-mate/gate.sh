#!/bin/bash
# Moo Mate 门禁 —— 脱敏 + go vet + go test
# 本地运行与存档 Gitea Actions workflow 共用同一实现。
# 用法：bash cmd/moo-mate/gate.sh [仓库根目录]
# 门范围：cmd/moo-mate（-I 自动跳过二进制产物）/ fnos-mate / skills/ai / build-mate.sh
# 退出码 0 = 全部门通过。
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="${1:-$(cd "$HERE/../.." && pwd)}"
cd "$REPO_ROOT" || exit 1
FAIL=0

# ---------- 门 1：脱敏扫描（0 命中放行）----------
# 公开模式与 Release 门同源（.github/sanitize-patterns.txt）。
# 真实凭据模式走本地专属文件 cmd/moo-mate/.gate-extra-patterns（gitignore，不入库）。
echo "=== 门 1: 脱敏扫描 ==="
read_patterns() { sed '1s/^\xef\xbb\xbf//' "$1" | grep -v '^[[:space:]]*#' | grep -v '^[[:space:]]*$'; }
PATTERNS="$(read_patterns .github/sanitize-patterns.txt 2>/dev/null)"
[ -f cmd/moo-mate/.gate-extra-patterns ] && PATTERNS="$PATTERNS
$(read_patterns cmd/moo-mate/.gate-extra-patterns)"
HITS=0
while IFS= read -r p; do
  [ -z "$p" ] && continue
  # 模式文件本身含字面量，排除自扫（同 Release 门 :exclude 做法）
  m=$(grep -rInE -e "$p" cmd/moo-mate fnos-mate skills/ai build-mate.sh 2>/dev/null | grep -v 'cmd/moo-mate/.gate-extra-patterns:' || true)
  if [ -n "$m" ]; then
    echo "[HIT] $p"
    echo "$m" | head -5
    HITS=$((HITS + $(printf '%s\n' "$m" | wc -l)))
  fi
done <<EOF
$PATTERNS
EOF
if [ "$HITS" -eq 0 ]; then
  echo "脱敏扫描: 0 命中 PASS"
else
  echo "脱敏扫描: 共 $HITS 处命中 FAIL"
  FAIL=1
fi

# ---------- 门 2：go vet ----------
echo "=== 门 2: go vet ==="
export PATH="/usr/local/bin:$PATH"
if go vet ./cmd/moo-mate/...; then
  echo "go vet PASS"
else
  echo "go vet FAIL"
  FAIL=1
fi

# ---------- 门 3：go test（moo-mate + 直接依赖包）----------
echo "=== 门 3: go test ==="
export MOO_DATA="$(mktemp -d)"
trap 'rm -rf "$MOO_DATA" 2>/dev/null' EXIT
if go test ./cmd/moo-mate/... ./internal/source/... ./internal/secret/... ./internal/config/... ./internal/netx/...; then
  echo "go test PASS"
else
  echo "go test FAIL"
  FAIL=1
fi

if [ "$FAIL" -eq 0 ]; then
  echo "GATE ALL PASS"
  exit 0
else
  echo "GATE FAILED"
  exit 1
fi
