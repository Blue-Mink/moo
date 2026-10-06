#!/bin/bash
# Moo — 构建脚本（x86_64 / arm64 双架构；0.6.303 起 arm 适配：
# 用法 bash build.sh [x86|arm|all]，默认 x86）
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
TARGET="${1:-x86}"
info "Moo v$VERSION (target=$TARGET, app=$APP_VERSION)"

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

# ── Step 2: 构建 Go 二进制（x86 + arm64，0.6.303 起 arm 适配）──────────────
info "构建 Go 二进制 (x86)..."
GOOS=linux GOARCH=amd64 go build -ldflags "-X main.Version=$APP_VERSION" -o "$BUILD_DIR/moo-server-x86" ./cmd/server/
info "构建 Go 二进制 (arm64)..."
GOOS=linux GOARCH=arm64 go build -ldflags "-X main.Version=$APP_VERSION" -o "$BUILD_DIR/moo-server-arm" ./cmd/server/
info "Go 构建完成"

# ── Step 3: 打包 FPK（pack_fpk <x86|arm>；arm 包 manifest platform=arm）────
TARGET="${1:-x86}"
pack_fpk() {
    local suffix="$1" server archline
    if [ "$suffix" = "arm" ]; then
        server="moo-server-arm"; archline="arm"
    else
        server="moo-server-x86"; archline="x86"
    fi
    info "打包 $suffix fpk (platform=$archline)..."
    local STAGING="$BUILD_DIR/tmp/fnos-$suffix"
    rm -rf "$STAGING"
    cp -a "$FNOS_DIR" "$STAGING"
    # manifest platform 单一事实源=x86；arm 包在 staging 里改写（不动源文件）
    sed -i "s/^platform[ \t]*=.*/platform        = $archline/" "$STAGING/manifest"

    cp "$BUILD_DIR/$server" "$STAGING/app/moo-server"
    if [ -d "$SCRIPT_DIR/web" ]; then
        cp -r "$SCRIPT_DIR/web" "$STAGING/app/web"
    fi

    cd "$SCRIPT_DIR"
    "$FNPACK" build --directory "$STAGING"
    local OUT_FPK="$SCRIPT_DIR/moo_${APP_VERSION}_${suffix}.fpk"
    mv "$SCRIPT_DIR/moo.fpk" "$OUT_FPK" 2>/dev/null || true
    [ -f "$OUT_FPK" ] || error "FPK 未生成: $OUT_FPK"
    info "✅ 产物: $OUT_FPK ($(du -h "$OUT_FPK" | cut -f1))"
    sha256sum "$OUT_FPK"
}
case "$TARGET" in
    x86) pack_fpk x86 ;;
    arm) pack_fpk arm ;;
    all) pack_fpk x86; pack_fpk arm ;;
    *)   error "未知构建目标: $TARGET（可选 x86 / arm / all）" ;;
esac
