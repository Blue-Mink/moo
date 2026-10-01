#!/bin/bash
# Moo — 构建脚本（仅 x86_64，按用户偏好不出 arm 版）
set -e
export COPYFILE_DISABLE=1

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BUILD_DIR="$SCRIPT_DIR/build"
FNOS_DIR="$SCRIPT_DIR/fnos"
FNPACK="$BUILD_DIR/fnpack"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; NC='\033[0m'
info()  { echo -e "${GREEN}[INFO]${NC} $1"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1" >&2; exit 1; }

cleanup() { rm -rf "$BUILD_DIR/tmp"; }
trap cleanup EXIT

mkdir -p "$BUILD_DIR/tmp"

# ── Step 0: fnpack ──────────────────────────────────────────────────────────
if [ ! -x "$FNPACK" ]; then
    info "fnpack 未找到，尝试下载..."
    curl -fsSL "https://static2.fnnas.com/fnpack/fnpack" -o "$FNPACK" && chmod +x "$FNPACK" \
        || { cp "$SCRIPT_DIR/../fnos-store/build/fnpack" "$FNPACK" 2>/dev/null && chmod +x "$FNPACK" \
        || error "fnpack 缺失且自动下载/拷贝失败，请手动放置到 $FNPACK"; }
fi

# 版本号取自 manifest（单一事实源，fnpack 硬约束整数格式）
VERSION=$(awk -F'= *' '/^version[ \t]*=/ {gsub(/ /,"",$2); print $2; exit}' "$FNOS_DIR/manifest")
[ -n "$VERSION" ] || error "manifest 中未找到 version"
# 应用内版本可带线标签（如 0.6.207-panel），FPK 文件名/Go Version 随之；
# 未设 MOO_APP_VERSION 时 = manifest 版本。
APP_VERSION="${MOO_APP_VERSION:-$VERSION}"
info "Moo v$VERSION (x86_64, app=$APP_VERSION)"

# ── Step 1: 构建前端 ────────────────────────────────────────────────────────
info "构建前端..."
if [ -d "$SCRIPT_DIR/frontend" ] && [ -f "$SCRIPT_DIR/frontend/package.json" ]; then
    cd "$SCRIPT_DIR/frontend"
    npm ci --silent
    npm run build
    cd "$SCRIPT_DIR"
    info "前端构建完成"
else
    warn "frontend/ 未就绪，跳过前端构建（web/ 使用现有产物或占位）"
fi

# ── Step 2: 构建 Go 二进制（仅 x86）────────────────────────────────────────
info "构建 Go 二进制 (x86)..."
GOOS=linux GOARCH=amd64 go build -ldflags "-X main.Version=$APP_VERSION" -o "$BUILD_DIR/moo-server-x86" ./cmd/server/
info "Go 构建完成"

# ── Step 3: 打包 x86 FPK ───────────────────────────────────────────────────
info "打包 x86 fpk..."
STAGING="$BUILD_DIR/tmp/fnos-x86"
rm -rf "$STAGING"
cp -a "$FNOS_DIR" "$STAGING"

cp "$BUILD_DIR/moo-server-x86" "$STAGING/app/moo-server"
if [ -d "$SCRIPT_DIR/web" ]; then
    cp -r "$SCRIPT_DIR/web" "$STAGING/app/web"
fi

# fnpack 用法（与 New Store 验证过的一致）：build --directory <staging>，产物 <appname>.fpk 落在 CWD
cd "$SCRIPT_DIR"
"$FNPACK" build --directory "$STAGING"
OUT_FPK="$SCRIPT_DIR/moo_${APP_VERSION}_x86.fpk"
mv "$SCRIPT_DIR/moo.fpk" "$OUT_FPK" 2>/dev/null || true
[ -f "$OUT_FPK" ] || error "FPK 未生成: $OUT_FPK"
info "✅ 产物: $OUT_FPK ($(du -h "$OUT_FPK" | cut -f1))"
sha256sum "$OUT_FPK"
