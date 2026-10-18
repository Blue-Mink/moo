#!/bin/sh
# 通用技能跨 Agent 安装器（POSIX sh，默认 dry-run）
# 用法：install.sh --target <名称> [--dest <目录>] [--dep <依赖技能,…>]
#                  [--link] [--apply] [--with-deps] [--list]
set -eu

SKILL_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
NAME=$(basename "$SKILL_DIR")
TARGET=""; DEST=""; LINK=0; APPLY=0; DEPS=""

usage() { sed -n '2,4p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-1}"; }

while [ $# -gt 0 ]; do
    case "$1" in
        --target) TARGET="${2:-}"; shift 2 ;;
        --dest)   DEST="${2:-}";   shift 2 ;;
        --dep)    DEPS="${DEPS:+$DEPS }$(echo "${2:-}" | tr ',' ' ')"; shift 2 ;;
        --with-deps) DEPS="${DEPS:+$DEPS }fn-fpk-builder"; shift ;;
        --link)   LINK=1; shift ;;
        --apply)  APPLY=1; shift ;;
        --list)   sed -n '/^# ---- 目标/,/^# ---- 目标结束/p' "$0" | sed 's/^# \{0,1\}//' | grep -v '^----'; exit 0 ;;
        -h|--help) usage 0 ;;
        *) echo "未知参数: $1" >&2; usage 1 ;;
    esac
done
[ -n "$TARGET" ] || usage 1

# ---- 目标
# 技能目录 : qwenpaw claude-code qwen-code agents-skills skills-dir
# 提示词   : codex gemini-cli hermes generic
# 项目规则 : agents-md cursor windsurf cline roo kilo continue augment copilot kiro openhands trae junie aider warp
# 组合     : all-project
# ---- 目标结束

FE=""; EXT=".md"
case "$TARGET" in
    qwenpaw)      KIND=dir; DEF="$PWD/skills" ;;
    claude-code)  KIND=dir; DEF="$HOME/.claude/skills" ;;
    qwen-code)    KIND=dir; DEF="$PWD/.qwen/skills" ;;
    agents-skills) KIND=dir; DEF="$PWD/.agents/skills" ;;
    skills-dir)   KIND=dir; DEF="" ;;
    codex)        KIND=prompt; DEF="$HOME/.codex/prompts" ;;
    gemini-cli)   KIND=prompt; DEF="$HOME/.gemini/commands" ;;
    hermes|generic) KIND=prompt; DEF="" ;;
    agents-md)    KIND=section; DEF="$PWD"; FILE="AGENTS.md" ;;
    cursor)       KIND=rule; DEF="$PWD/.cursor/rules"; FE="alwaysApply: true"; EXT=".mdc" ;;
    windsurf)     KIND=rule; DEF="$PWD/.windsurf/rules"; FE="trigger: always_on" ;;
    cline)        KIND=rule; DEF="$PWD/.clinerules" ;;
    roo)          KIND=rule; DEF="$PWD/.roo/rules" ;;
    kilo)         KIND=rule; DEF="$PWD/.kilo/rules" ;;
    continue)     KIND=rule; DEF="$PWD/.continue/rules" ;;
    augment)      KIND=rule; DEF="$PWD/.augment/rules" ;;
    openhands)    KIND=rule; DEF="$PWD/.openhands/microagents" ;;
    copilot)      KIND=rule; DEF="$PWD/.github/instructions"; FE='applyTo: "**"'; EXT=".instructions.md" ;;
    kiro)         KIND=rule; DEF="$PWD/.kiro/steering"; FE="inclusion: always" ;;
    trae)         KIND=section; DEF="$PWD"; FILE=".trae/rules/project_rules.md" ;;
    junie)        KIND=section; DEF="$PWD"; FILE=".junie/guidelines.md" ;;
    aider)        KIND=section; DEF="$PWD"; FILE="CONVENTIONS.md" ;;
    warp)         KIND=section; DEF="$PWD"; FILE="WARP.md" ;;
    all-project)  KIND=multi; DEF="$PWD" ;;
    *) echo "不支持的目标: $TARGET（--list 查看）" >&2; exit 1 ;;
esac

[ "$KIND" = multi ] || { [ -n "$DEST" ] || DEST="$DEF"; }
if [ -z "$DEST" ] && [ "$KIND" != multi ]; then
    echo "target=$TARGET 需要 --dest" >&2; exit 1
fi

DESC=$(sed -n 's/^description: *//p' "$SKILL_DIR/SKILL.md" | head -1 | sed 's/^"//; s/"$//')

echo "技能     : $NAME"
echo "目录     : $SKILL_DIR"
echo "目标     : $TARGET"
if [ "$KIND" != multi ]; then echo "落地     : $DEST"; fi
if [ -n "$DEPS" ]; then echo "依赖     : $DEPS"; fi
if [ "$APPLY" -eq 1 ]; then echo "模式     : 执行"; else echo "模式     : 预演-dry-run"; fi

run() {
    echo "  + $*"
    if [ "$APPLY" -eq 1 ]; then "$@"; fi
    return 0
}

ensure_outside() {
    p=$1
    if command -v python3 >/dev/null 2>&1; then
        abs=$(python3 -c 'import os,sys;print(os.path.realpath(sys.argv[1]))' "$p")
    else
        abs="$p"
    fi
    case "$abs" in
        "$SKILL_DIR"|"$SKILL_DIR"/*) echo "!! 目标落在技能自身内（$abs）" >&2; exit 1 ;;
    esac
}
if [ "$KIND" != prompt ] && [ -n "$DEST" ]; then ensure_outside "$DEST"; fi

copy_tree() { # src dst —— 排除 VCS / 推送包 / 缓存
    src=$1; dst=$2
    mkdir -p "$dst"
    ( cd "$src" && tar \
        --exclude=.git --exclude='.pushpack*' --exclude=__pycache__ \
        --exclude='*.pyc' --exclude=node_modules --exclude=.DS_Store \
        -cf - . ) | ( cd "$dst" && tar -xf - )
}

install_one() { # src name
    src=$1; nm=$2
    if [ "$LINK" -eq 1 ]; then
        echo "  + 链接 $DEST/$nm"
        if [ "$APPLY" -eq 1 ]; then ln -sfn "$src" "$DEST/$nm"; fi
    else
        echo "  + 复制 $DEST/$nm"
        if [ "$APPLY" -eq 1 ]; then
            rm -rf "$DEST/$nm"
            copy_tree "$src" "$DEST/$nm"
        fi
    fi
}

bundle_repo_assets() {
    # 仓库内安装 fpk-build-to-moo 时附带生成器与协议文档
    if [ "$NAME" != "fpk-build-to-moo" ]; then return 0; fi
    if [ "$LINK" -eq 1 ]; then return 0; fi
    root=$(CDPATH= cd -- "$SKILL_DIR/../.." 2>/dev/null && pwd) || return 0
    if [ ! -f "$root/tools/gen-repo-moo-json.py" ]; then return 0; fi
    echo "  + 附带生成器与协议文档 → $DEST/$NAME/bundled/"
    if [ "$APPLY" -eq 1 ]; then
        mkdir -p "$DEST/$NAME/bundled/tools" "$DEST/$NAME/bundled/docs"
        cp -R "$root/tools/gen-moo-json" "$DEST/$NAME/bundled/tools/" 2>/dev/null || true
        cp "$root/tools/gen-repo-moo-json.py" "$DEST/$NAME/bundled/tools/" 2>/dev/null || true
        cp "$root/docs/Moo应用源协议.md" "$DEST/$NAME/bundled/docs/" 2>/dev/null || true
    fi
}

install_deps() {
    if [ -z "$DEPS" ]; then return 0; fi
    for d in $DEPS; do
        dep="$SKILL_DIR/../$d"
        if [ ! -d "$dep" ]; then
            echo "  = 未找到依赖技能 $d（跳过）"
            continue
        fi
        install_one "$dep" "$d"
    done
}

install_prompt() {
    run mkdir -p "$DEST"
    # 优先使用技能自带的适配器模板（adapters/<target>/prompt.md）
    tpl=""
    case "$TARGET" in
        codex) tpl="$SKILL_DIR/adapters/codex/prompt.md" ;;
        hermes|generic)
            tpl="$SKILL_DIR/adapters/hermes/prompt.md"
            if [ ! -f "$tpl" ]; then tpl="$SKILL_DIR/adapters/generic/PROMPT.md"; fi
            ;;
    esac
    if [ -n "$tpl" ] && [ -f "$tpl" ]; then
        out="$DEST/$NAME.md"
        echo "  + 生成 $out（来自 ${tpl#$SKILL_DIR/}）"
        if [ "$APPLY" -eq 1 ]; then
            sed "s#{{SKILL_DIR}}#$SKILL_DIR#g" "$tpl" > "$out"
        fi
        return 0
    fi
    if [ "$TARGET" = "gemini-cli" ]; then
        out="$DEST/$NAME.toml"
        echo "  + 生成 $out"
        if [ "$APPLY" -eq 1 ]; then
            {
                printf 'description = "%s"\nprompt = %s\n' "$DESC" "'''"
                printf '读取 %s/SKILL.md 并严格按其流程执行。\n\n%s\n' "$SKILL_DIR" "$DESC"
                printf "'''\n"
            } > "$out"
        fi
    else
        out="$DEST/$NAME.md"
        echo "  + 生成 $out"
        if [ "$APPLY" -eq 1 ]; then
            {
                printf -- '---\nname: %s\ndescription: "%s"\n---\n\n' "$NAME" "$DESC"
                printf '开始前先完整读取 `%s/SKILL.md`，按其中的流程与硬规则执行；细则在 `references/`，脚本在 `scripts/`。\n' "$SKILL_DIR"
                printf '需要用户决定的地方给编号选项（1/2/3）；写操作前确认。\n'
            } > "$out"
        fi
    fi
}

install_rules() {
    run mkdir -p "$DEST"
    out="$DEST/$NAME$EXT"
    echo "  + 生成 $out"
    if [ "$APPLY" -eq 1 ]; then
        {
            printf -- '---\ndescription: "%s"\n%s\n---\n\n' "$NAME" "$FE"
            printf '# %s 技能\n\n' "$NAME"
            printf '%s\n\n' "$DESC"
            printf '任务涉及该领域时，读取 `%s/SKILL.md` 并遵循；细则在 `references/`。\n' "$SKILL_DIR"
            printf '写操作前向用户确认；需要选择时给编号选项。\n'
        } > "$out"
    fi
}

install_section() {
    f="$DEST/$FILE"
    run mkdir -p "$(dirname "$f")"
    if [ -f "$f" ] && grep -q "$NAME:begin" "$f" 2>/dev/null; then
        echo "  = $f 已有本技能段落，跳过"
        return 0
    fi
    echo "  + 追加技能段落到 $f"
    if [ "$APPLY" -eq 1 ]; then
        {
            printf '\n<!-- %s:begin -->\n## 技能：%s\n\n' "$NAME" "$NAME"
            printf -- '- **%s**：`%s/SKILL.md`\n\n' "$NAME" "$SKILL_DIR"
            printf '任务涉及该领域时先读 SKILL.md，按其中流程与硬规则执行。\n'
            printf '<!-- %s:end -->\n' "$NAME"
        } >> "$f"
    fi
}

case "$KIND" in
    dir)
        run mkdir -p "$DEST"
        install_one "$SKILL_DIR" "$NAME"
        bundle_repo_assets
        install_deps
        ;;
    prompt)  install_prompt ;;
    rule)    install_rules ;;
    section) install_section ;;
    multi)
        echo "  组合安装（忽略 --dest，写入当前项目）"
        for t in agents-md cursor windsurf cline continue copilot kiro; do
            if [ "$APPLY" -eq 1 ]; then
                sh "$0" --target "$t" --apply >/dev/null || echo "  !! $t 失败" >&2
            else
                sh "$0" --target "$t" | grep '^  [+=]'
            fi
        done
        ;;
esac

echo
if [ "$APPLY" -eq 1 ]; then echo "完成。"; else echo "预演结束（未写入任何文件）。加 --apply 执行。"; fi
