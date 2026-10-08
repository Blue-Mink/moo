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

# ── Step 0.5: 生成内嵌发布说明（0.6.311r：关于页卡片跟随最新版本日志）─────
# 单一事实源 = moo.json 顶层 version/changelog（每次发版同步更新）；
# 版本不匹配/文件缺失/解析失败 → 置空（卡片回落源条目，不阻塞构建）。
if command -v python3 >/dev/null 2>&1 && [ -f "$SCRIPT_DIR/moo.json" ]; then
    python3 - "$SCRIPT_DIR/moo.json" "$APP_VERSION" "$SCRIPT_DIR/internal/api/changelog_current.json" <<'PY'
import json, sys
src, ver, out = sys.argv[1], sys.argv[2], sys.argv[3]
text = ""
try:
    with open(src, encoding="utf-8") as f:
        m = json.load(f)["apps"]["moo"]
    if m.get("version") == ver:
        text = (m.get("changelog") or "").strip()
except Exception:
    text = ""
with open(out, "w", encoding="utf-8") as f:
    json.dump({"version": ver, "changelog": text}, f, ensure_ascii=False, indent=1)
    f.write("\n")
PY
    info "内嵌当前版本发布说明 -> internal/api/changelog_current.json"
else
    warn "python3 或 moo.json 缺失，跳过内嵌发布说明（关于页卡片回落源条目）"
fi

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

# ── Step 1.5: 刷新固定推荐快照（0.6.314 C，构建期抓取）─────────────────────
# gen_reco_snapshots.py 从三源（raw 主 + jsDelivr 备）抓取固定前三推荐
# （fnos-apps-store / fndepot / fn-knock）完整元数据 + README 全文 +
# 图标 base64 → internal/reco/snapshots.json（go:embed，必须早于 Step 2）。
# 离线 / 网络失败 / python3 缺失 → 保留仓库已有快照，不阻塞构建。
if command -v python3 >/dev/null 2>&1 && [ -f "$SCRIPT_DIR/gen_reco_snapshots.py" ]; then
    if python3 "$SCRIPT_DIR/gen_reco_snapshots.py" --out "$SCRIPT_DIR/internal/reco/snapshots.json"; then
        info "固定推荐快照已刷新 -> internal/reco/snapshots.json"
    else
        warn "固定推荐快照刷新失败（保留仓库已有文件）——见上方脚本输出"
    fi
else
    warn "python3 或 gen_reco_snapshots.py 缺失，跳过固定推荐快照刷新"
fi

# ── Step 2: 构建 Go 二进制（x86 + arm64，0.6.303 起 arm 适配）──────────────
info "构建 Go 二进制 (x86)..."
# -s -w：剥离符号表+DWARF（0.6.304 起 FPK 精简）：二进制 15.6M→11.1M，
# 运行时行为零变化，panic 堆栈仍带函数名+文件:行（.gopclntab 不剥，已实测）。
GOOS=linux GOARCH=amd64 go build -ldflags "-s -w -X main.Version=$APP_VERSION" -o "$BUILD_DIR/moo-server-x86" ./cmd/server/
info "构建 Go 二进制 (arm64)..."
GOOS=linux GOARCH=arm64 go build -ldflags "-s -w -X main.Version=$APP_VERSION" -o "$BUILD_DIR/moo-server-arm" ./cmd/server/
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
    # 0.6.304 起：不再把 web/ 打进 app.tgz。前端经 web/embed.go 的 go:embed
    # 全量内嵌进二进制（server.go 无磁盘回退分支），FPK 里的 web/ 目录自 0.6.258
    # 引入 embed 起即死重（运行态从未读取）。去掉后 app.tgz −1.2M、FPK −368KB。

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
