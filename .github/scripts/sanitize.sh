#!/bin/bash
# sanitize.sh — 脱敏扫描（唯一实现，CI 与本地 moo-gate.sh 共用）
#
# 为什么要抽出来：此前这段逻辑只存在于 release.yml 的 inline run 里，本地无法复现，
# 于是「本地绿灯、CI 红灯」或「CI 松弛、本地更严」都可能悄悄发生（门禁漂移）。
#
# ⚠️ 必须带 --untracked（2026-10-31 修，P0）：
#    git grep 默认只搜「已跟踪」文件，于是「新写但还没 git add」的文件完全不扫——
#    推之前跑门禁等于没跑（实测：造一个含内网 IP + PAT 的未跟踪探针，本脚本仍报
#    「0 命中 ✓」；加 --untracked 才拦住）。而漏 add 恰恰是最常见的失误形态，
#    所以这层必须覆盖 untracked；git 按 .gitignore 跳过 node_modules/FPK 等，不会拖慢。
#    CI 侧 checkout 后没有 untracked，该参数无副作用（行为与原来一致）。
#
# 用法：bash .github/scripts/sanitize.sh            # 扫整树（含未跟踪）
#       EXTRA_PATTERNS='...' bash .../sanitize.sh   # 追加私密模式（CI 用 vars/secrets）
# 退出码：0 = 0 命中；1 = 有命中或 git grep 自身异常（fail-closed）
set -uo pipefail

cd "$(git rev-parse --show-toplevel 2>/dev/null || echo .)" || exit 1
PATS=.github/sanitize-patterns.txt
if [ ! -f "$PATS" ]; then
  echo "::error::找不到模式文件 $PATS" >&2
  exit 1
fi
if [ -n "${EXTRA_PATTERNS:-}" ]; then
  TMP="$(mktemp)"
  cat "$PATS" > "$TMP"
  printf '\n%s\n' "$EXTRA_PATTERNS" >> "$TMP"
  PATS="$TMP"
fi
echo "sanitize: $(grep -Ev '^[[:space:]]*(#|$)' "$PATS" | wc -l) 条模式（公开 + 扩展）"
fails=0
while IFS= read -r pat; do
  # 剥 UTF-8 BOM：否则第一条模式会变成整行注释文本（假绿埋雷）
  pat="${pat#$'\xef\xbb\xbf'}"
  case "$pat" in '' | \#*) continue ;; esac
  # -e 防以 - 开头的模式被当成 git 选项；rc>1 = git grep 自身异常 → fail-closed
  rc=0
  m=$(git grep --untracked -I -n -E -e "$pat" -- . ':(exclude).github/sanitize-patterns.txt' 2>&1) || rc=$?
  if [ $rc -gt 1 ]; then
    echo "::error::git grep 异常（rc=$rc）模式: $pat —— 按失败处理（fail-closed）"
    echo "$m"
    exit 1
  fi
  if [ $rc -eq 0 ] && [ -n "$m" ]; then
    echo "::error title=脱敏命中::模式: $pat"
    echo "$m"
    fails=$((fails+1))
  fi
done < "$PATS"
if [ "$fails" -gt 0 ]; then
  echo "::error::共 $fails 个敏感模式命中，发布流程终止"
  exit 1
fi
echo "sanitize: 0 命中 ✓"
