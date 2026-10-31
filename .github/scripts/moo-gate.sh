#!/bin/bash
# moo-gate.sh — Moo 单一门禁入口（RELEASE_GATE P1）
#
# 目的：消灭「本地跑的是子集、CI 跑的是另一套」这种漂移。
# 注意步骤顺序：前端必须先构建（web/dist 供 go:embed，缺失时 vet/test 编译不过），
# 这与 release.yml gate job 的真实顺序一致——顺序错了本地有缓存会假绿、CI 会真红。
# 这里按 release.yml gate job 的**同一顺序、同一命令**执行；逻辑不同的部分
# （脱敏）直接调用同一个 .github/scripts/sanitize.sh，不复制。
#
# 用法：
#   bash .github/scripts/moo-gate.sh                    # 全量（约 3~6 分钟）
#   bash .github/scripts/moo-gate.sh --quick            # 跳前端安装与 go test
#   bash .github/scripts/moo-gate.sh --tag 0.6.378      # 额外跑版本契约（phase=index）
#   bash .github/scripts/moo-gate.sh --tag 0.6.378 --phase code
#
# 退出码：0 全绿；1 有任一项失败
set -uo pipefail

cd "$(git rev-parse --show-toplevel 2>/dev/null || echo .)" || exit 1

QUICK=0
SKIP_FRONTEND=0
TAG=""
PHASE="index"
FNDEPOT=""
while [ $# -gt 0 ]; do
  case "$1" in
    --quick) QUICK=1 ;;
    --no-frontend) SKIP_FRONTEND=1 ;;
    --tag) TAG="${2:-}"; shift ;;
    --phase) PHASE="${2:-index}"; shift ;;
    --fndepot) FNDEPOT="${2:-}"; shift ;;
    -h | --help) sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "未知参数：$1" >&2; exit 2 ;;
  esac
  shift
done

# internal/api 的 readme 磁盘缓存会落在包内 moo-data/，重复跑会命中旧缓存假失败
# （已知 flake）。默认给一个临时数据目录，保证门禁结果可重复；显式设置 MOO_DATA 则尊重之。
export MOO_DATA="${MOO_DATA:-$(mktemp -d /tmp/moo-gate-data.XXXXXX)}"

FAILED=()
step() { printf '\n[gate] ==> %s\n' "$1"; }
ok() { printf '[gate] ✓ %s\n' "$1"; }
bad() { printf '[gate] ✗ %s\n' "$1"; FAILED+=("$1"); }

# ── 1 脱敏（唯一实现，CI 共用）────────────────────────────────────────────
step "sanitize"
if bash .github/scripts/sanitize.sh; then ok sanitize; else bad sanitize; fi

# ── 1.5 引用完整性（2026-10-31 P0 治本）───────────────────────────────────
# 被代码/流水线引用、但没入库的文件（//go:embed 数据、workflow 调用的脚本、
# eslint flat config），只会在 CI 上炸。这里提前拦。
# 注意：本地判据是「已 git add / 已跟踪」，所以**本地未 add 时会红**（这是设计，
# 本地更严）；CI checkout 后全部文件天然 tracked，该步恒为绿，不会误伤发布。
step "refs"
if python3 .github/scripts/check_refs.py; then ok refs; else bad "refs（被引用但未入库，见上）"; fi

# ── 2 前端（构建产物供 go:embed；单测 + lint）─────────────────────────────
if [ "$QUICK" -eq 1 ] || [ "$SKIP_FRONTEND" -eq 1 ]; then
  echo "[gate] --skip frontend"
elif [ ! -f frontend/package.json ]; then
  echo "[gate] 无 frontend/package.json，跳过"
else
  step "frontend"
  FRONT_OK=1
  if [ ! -d frontend/node_modules ]; then
    (cd frontend && npm ci) || FRONT_OK=0
  fi
  [ "$FRONT_OK" -eq 1 ] && { (cd frontend && npm run build) || FRONT_OK=0; }
  [ "$FRONT_OK" -eq 1 ] && { (cd frontend && npm run test:unit) || FRONT_OK=0; }
  [ "$FRONT_OK" -eq 1 ] && { (cd frontend && npm run lint) || FRONT_OK=0; }
  if [ "$FRONT_OK" -eq 1 ]; then ok frontend; else bad "frontend（build/test:unit/lint）"; fi
fi

# ── 3 gofmt ───────────────────────────────────────────────────────────────
step "gofmt"
OUT=$(gofmt -l cmd internal tools 2>/dev/null)
if [ -n "$OUT" ]; then
  echo "$OUT"
  bad "gofmt（未格式化文件见上）"
else ok gofmt; fi

# ── 4 go vet ──────────────────────────────────────────────────────────────
step "go vet"
PKGS=$(go list ./... 2>/dev/null | grep -v node_modules)
if [ -z "$PKGS" ]; then
  bad "go list（没有包或 go 不在 PATH）"
elif go vet $PKGS; then ok "go vet（$(echo "$PKGS" | wc -l) 包）"; else bad "go vet"; fi

# ── 5 go test ─────────────────────────────────────────────────────────────
if [ "$QUICK" -eq 1 ]; then
  echo "[gate] --skip go test（--quick）"
elif go test $PKGS; then ok "go test"; else bad "go test"; fi

# ── 6 版本契约（按需；P2）────────────────────────────────────────────────
if [ -n "$TAG" ]; then
  step "release contract (phase=$PHASE tag=$TAG)"
  ARGS=(--tag "$TAG" --phase "$PHASE" --dist .)
  [ -n "$FNDEPOT" ] && ARGS+=(--fndepot "$FNDEPOT")
  if python3 .github/scripts/verify_release_contract.py "${ARGS[@]}"; then
    ok "release contract"
  else
    bad "release contract"
  fi
fi

# ── 汇总 ──────────────────────────────────────────────────────────────────
printf '\n[gate] ================ 汇总 ================\n'
if [ ${#FAILED[@]} -eq 0 ]; then
  echo "[gate] GATE ALL PASS"
  exit 0
fi
printf '[gate] 失败项：%s\n' "${FAILED[*]}"
exit 1
