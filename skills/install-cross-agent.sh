#!/bin/sh
# Moo 仓库技能跨 Agent 安装（POSIX sh，默认 dry-run）—— 调度器
# 用法：install-cross-agent.sh --target <名称> [--skill all|moo,fpk-build-to-moo]
#                              [--dest <目录>] [--dep <技能,…>] [--link] [--apply] [--include-ai] [--list]
#
# 本脚本只负责「选技能 + 转发」；真正落盘逻辑在各技能自带的 scripts/install.sh，
# 避免两套实现漂移。
set -eu

SKILLS_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
TARGET=""; DEST=""; LINK=0; APPLY=0; INCLUDE_AI=0; DEPS=""; SKILLS="all"

usage() { sed -n '2,4p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-1}"; }

while [ $# -gt 0 ]; do
    case "$1" in
        --target) TARGET="${2:-}"; shift 2 ;;
        --skill)  SKILLS="${2:-}"; shift 2 ;;
        --dest)   DEST="${2:-}";   shift 2 ;;
        --dep)    DEPS="${DEPS:+$DEPS }$(echo "${2:-}" | tr ',' ' ')"; shift 2 ;;
        --with-deps) DEPS="${DEPS:+$DEPS }fn-fpk-builder"; shift ;;
        --link)   LINK=1; shift ;;
        --apply)  APPLY=1; shift ;;
        --include-ai) INCLUDE_AI=1; shift ;;
        --list)   sed -n '/^# ---- 目标/,/^# ---- 目标结束/p' "$0" | sed 's/^# \{0,1\}//' | grep -v '^----'; exit 0 ;;
        -h|--help) usage 0 ;;
        *) echo "未知参数: $1" >&2; usage 1 ;;
    esac
done
[ -n "$TARGET" ] || usage 1
[ "$INCLUDE_AI" -eq 1 ] && SKILLS="$SKILLS,ai"

# ---- 目标
# 技能目录 : qwenpaw claude-code qwen-code agents-skills skills-dir
# 提示词   : codex gemini-cli hermes generic
# 项目规则 : agents-md cursor windsurf cline roo kilo continue augment copilot kiro openhands trae junie aider warp
# 组合     : all-project
# ---- 目标结束

case "$TARGET" in
    qwenpaw|claude-code|qwen-code|agents-skills|skills-dir) KIND=dir ;;
    codex|gemini-cli|hermes|generic) KIND=prompt ;;
    cursor|windsurf|cline|roo|kilo|continue|augment|copilot|kiro|openhands) KIND=rule ;;
    trae|junie|aider|warp) KIND=section ;;
    agents-md) KIND=agentsmd ;;
    all-project) KIND=multi ;;
    *) echo "不支持的目标: $TARGET（--list 查看）" >&2; exit 1 ;;
esac

# 选定技能（只认带 SKILL.md 且有自带安装器的目录）
want() {
    case ",${SKILLS}," in
        *,all,*) return 0 ;;
        *,"$1",*) return 0 ;;
        *) return 1 ;;
    esac
}
SKILL_LIST=""
for d in "$SKILLS_DIR"/*/; do
    n=$(basename "$d")
    [ -f "$d/SKILL.md" ] || continue
    [ -x "$d/scripts/install.sh" ] || [ -f "$d/scripts/install.sh" ] || continue
    want "$n" && SKILL_LIST="$SKILL_LIST $n"
done
SKILL_LIST=${SKILL_LIST# }
[ -n "$SKILL_LIST" ] || { echo "没有匹配到可安装技能（--skill $SKILLS）" >&2; exit 1; }

# 组合安装时忽略 --dest（按各 Agent 约定写入当前项目）
if [ "$KIND" != multi ] && [ -z "$DEST" ]; then
    case "$TARGET" in
        qwenpaw) DEST="$PWD/skills" ;;
        claude-code) DEST="$HOME/.claude/skills" ;;
        qwen-code) DEST="$PWD/.qwen/skills" ;;
        agents-skills) DEST="$PWD/.agents/skills" ;;
        codex) DEST="$HOME/.codex/prompts" ;;
        gemini-cli) DEST="$HOME/.gemini/commands" ;;
        cursor) DEST="$PWD/.cursor/rules" ;;
        windsurf) DEST="$PWD/.windsurf/rules" ;;
        cline) DEST="$PWD/.clinerules" ;;
        roo) DEST="$PWD/.roo/rules" ;;
        kilo) DEST="$PWD/.kilo/rules" ;;
        continue) DEST="$PWD/.continue/rules" ;;
        augment) DEST="$PWD/.augment/rules" ;;
        openhands) DEST="$PWD/.openhands/microagents" ;;
        copilot) DEST="$PWD/.github/instructions" ;;
        kiro) DEST="$PWD/.kiro/steering" ;;
        trae|junie|aider|warp) DEST="$PWD" ;;
        agents-md) DEST="$PWD" ;;
    esac
fi
if [ "$KIND" != multi ] && [ -z "$DEST" ]; then
    echo "target=$TARGET 需要 --dest" >&2; exit 1
fi

ensure_outside() {
    p=$1
    if command -v python3 >/dev/null 2>&1; then
        abs=$(python3 -c 'import os,sys;print(os.path.realpath(sys.argv[1]))' "$p")
    else
        abs="$p"
    fi
    case "$abs" in
        "$SKILLS_DIR"|"$SKILLS_DIR"/*) echo "!! 目标落在技能源目录内（$abs），请在项目根运行或用 --dest" >&2; exit 1 ;;
    esac
}
[ "$KIND" = prompt ] || [ "$KIND" = multi ] || ensure_outside "$DEST"

echo "技能目录 : $SKILLS_DIR"
echo "技能     : $SKILL_LIST"
echo "目标     : $TARGET"
[ "$KIND" = multi ] || echo "落地     : $DEST"
[ -n "$DEPS" ] && echo "依赖     : $DEPS"
echo "模式     : $([ "$APPLY" -eq 1 ] && echo 执行 || echo 预演-dry-run)"

invoke() { # skill target [extra args...]
    s=$1; t=$2; shift 2
    if [ "$APPLY" -eq 1 ]; then
        sh "$SKILLS_DIR/$s/scripts/install.sh" --target "$t" "$@" --apply
    else
        sh "$SKILLS_DIR/$s/scripts/install.sh" --target "$t" "$@" | grep '^  [+=]'
    fi
}

DEP_ARG=""
for d in $DEPS; do DEP_ARG="${DEP_ARG:+$DEP_ARG,}$d"; done

case "$KIND" in
    dir)
        for s in $SKILL_LIST; do
            if [ "$LINK" -eq 1 ] && [ -n "$DEP_ARG" ]; then
                invoke "$s" skills-dir --dest "$DEST" --link --dep "$DEP_ARG"
            elif [ "$LINK" -eq 1 ]; then
                invoke "$s" skills-dir --dest "$DEST" --link
            elif [ -n "$DEP_ARG" ]; then
                invoke "$s" skills-dir --dest "$DEST" --dep "$DEP_ARG"
            else
                invoke "$s" skills-dir --dest "$DEST"
            fi
        done
        ;;
    prompt|rule|agentsmd|section)
        for s in $SKILL_LIST; do
            invoke "$s" "$TARGET" --dest "$DEST"
        done
        ;;
    multi)
        echo "  组合安装（按各 Agent 约定写入当前项目）"
        for t in agents-md cursor windsurf cline continue copilot kiro; do
            for s in $SKILL_LIST; do
                invoke "$s" "$t"
            done
        done
        ;;
esac

echo
if [ "$APPLY" -eq 1 ]; then echo "完成。"; else echo "预演结束（未写入任何文件）。加 --apply 执行。"; fi
