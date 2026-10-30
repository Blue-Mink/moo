#!/bin/bash
# Moo Mate — 构建脚本（x86；图标复用 Moo，应用名 moo-mate）。
# 用法：bash build-mate.sh [x86|arm]
set -e
export COPYFILE_DISABLE=1

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BUILD_DIR="$SCRIPT_DIR/build"
FNOS_DIR="$SCRIPT_DIR/fnos-mate"
FNPACK="$BUILD_DIR/fnpack"

info()  { echo -e "\033[0;32m[INFO]\033[0m $1"; }
error() { echo -e "\033[0;31m[ERROR]\033[0m $1" >&2; exit 1; }

mkdir -p "$BUILD_DIR/tmp"

# fnpack 存在性（复用 Moo 的 build/fnpack）
[ -x "$FNPACK" ] || error "fnpack 缺失：$FNPACK（先跑一次 Moo 的 build.sh 下载）"

# 版本号取自 manifest（单一事实源）
VERSION=$(awk -F'= *' '/^version[ \t]*=/ {gsub(/ /,"",$2); print $2; exit}' "$FNOS_DIR/manifest")
[ -n "$VERSION" ] || error "manifest 中未找到 version"
APP_VERSION="${MOO_MATE_VERSION:-$VERSION}"
TARGET="${1:-x86}"
info "Moo Mate v$VERSION (target=$TARGET)"

# Step 2: 构建 Go 二进制（x86；伴侣默认只打 x86）
info "构建 Go 二进制 (x86)..."
GOOS=linux GOARCH=amd64 go build -ldflags "-s -w -X main.buildVersion=$APP_VERSION" -o "$BUILD_DIR/moo-mate-x86" ./cmd/moo-mate/
info "Go 构建完成"

# Step 3: 打包
pack_fpk() {
    local suffix="$1" server archline
    if [ "$suffix" = "arm" ]; then
        server="moo-mate-arm"; archline="arm"
    else
        server="moo-mate-x86"; archline="x86"
    fi
    info "打包 $suffix fpk (platform=$archline)..."
    local STAGING="$BUILD_DIR/tmp/fnos-mate-$suffix"
    rm -rf "$STAGING"
    cp -a "$FNOS_DIR" "$STAGING"
    sed -i "s/^platform[ \t]*=.*/platform        = $archline/" "$STAGING/manifest"
    cp "$BUILD_DIR/$server" "$STAGING/app/moo-mate"
    cd "$SCRIPT_DIR"
    "$FNPACK" build --directory "$STAGING"
    local OUT_FPK="$SCRIPT_DIR/moo-mate_${APP_VERSION}_${suffix}.fpk"
    mv "$SCRIPT_DIR/moo-mate.fpk" "$OUT_FPK" 2>/dev/null || true
    [ -f "$OUT_FPK" ] || error "FPK 未生成: $OUT_FPK"
    info "✅ 产物: $OUT_FPK ($(du -h "$OUT_FPK" | cut -f1))"
    sha256sum "$OUT_FPK"
}

case "$TARGET" in
    x86) pack_fpk x86 ;;
    arm)
        info "构建 Go 二进制 (arm64)..."
        GOOS=linux GOARCH=arm64 go build -ldflags "-s -w -X main.buildVersion=$APP_VERSION" -o "$BUILD_DIR/moo-mate-arm" ./cmd/moo-mate/
        pack_fpk arm ;;
    *) error "未知构建目标: $TARGET（可选 x86 / arm）" ;;
esac
