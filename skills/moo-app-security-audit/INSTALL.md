# 安装：moo-app-security-audit（跨 Agent）

本技能只依赖 **Markdown + python3 标准库 + curl**，不绑定任何平台专有 API。
落地形态有三种：**技能目录（SKILL.md）**、**提示词/命令**、**项目规则文件**。

## 一、一键安装

```bash
S=/path/to/moo-app-security-audit/scripts/install.sh
sh $S --list                    # 查看全部目标
sh $S --target cursor           # 默认 dry-run，只打印计划
sh $S --target cursor --apply   # 真正写入
```

| 参数 | 作用 |
|---|---|
| `--target <名称>` | 目标平台（见下表） |
| `--dest <目录>` | 覆盖默认落地路径（Hermes / 通用 Agent 必填） |
| `--link` | 用符号链接代替复制（仅技能目录类；升级本目录即生效） |
| `--apply` | 真正写入（默认只预演） |
| `--list` | 打印支持的目标 |

> 规则文件类目标默认写**当前目录**（请在项目根运行）；脚本会拒绝把文件写进技能自身目录。
> `--target all-project` 一次写入：`AGENTS.md` + cursor + windsurf + cline + copilot + continue + kiro。

## 二、目标矩阵

### 1. 技能目录（复制整个技能，含 scripts/references）

| target | 默认路径 | 说明 |
|---|---|---|
| `qwenpaw` | `<项目>/skills/` 或 `--dest` 工作区 | QwenPaw 原生 SKILL.md；之后 `qwenpaw skills enable` |
| `claude-code` | `~/.claude/skills/` | Claude Code 技能目录（frontmatter 兼容） |
| `qwen-code` | `<项目>/.qwen/skills/` | Qwen Code Agent Skills（官方文档 `.qwen/skills/`） |
| `agents-skills` | `<项目>/.agents/skills/` | 社区通用技能位（Goose/Zed/OpenCode/Gemini CLI 等按各自版本识别） |
| `skills-dir` | 需 `--dest` | 任意支持「目录 + SKILL.md」的 Agent |

### 2. 提示词 / 命令

| target | 默认路径 | 说明 |
|---|---|---|
| `codex` | `~/.codex/prompts/moo-app-security-audit.md` | Codex CLI 自定义提示词 |
| `gemini-cli` | `~/.gemini/commands/moo-app-security-audit.toml` | Gemini CLI 自定义命令 `/moo-app-security-audit` |
| `hermes` / `generic` | 需 `--dest` | 技能目录 + 提示词（爱马仕等通用 Agent） |

### 3. 项目规则文件（写入项目，随仓库走）

| target | 默认路径 | 说明 |
|---|---|---|
| `agents-md` | `AGENTS.md` | **通用标准**（Codex/Cursor/Claude Code 回退/Continue/Aider/OpenHands/Copilot agent/Kiro 等） |
| `cursor` | `.cursor/rules/moo-app-security-audit.mdc` | `alwaysApply: true` |
| `windsurf` | `.windsurf/rules/moo-app-security-audit.md` | `trigger: always_on`；旧版 `.windsurfrules` 也可识别 |
| `cline` | `.clinerules/moo-app-security-audit.md` | Cline 规则目录；也会自动识别 `.cursorrules`/`.windsurfrules`/`AGENTS.md` |
| `roo` | `.roo/rules/moo-app-security-audit.md` | Roo Code；旧版单文件 `.roorules` |
| `kilo` | `.kilo/rules/moo-app-security-audit.md` | Kilo Code；旧路径 `.kilocode/rules/` 作回退 |
| `continue` | `.continue/rules/moo-app-security-audit.md` | Continue.dev |
| `augment` | `.augment/rules/moo-app-security-audit.md` | Augment Code |
| `kiro` | `.kiro/steering/moo-app-security-audit.md` | `inclusion: always` |
| `copilot` | `.github/instructions/moo-app-security-audit.instructions.md` | `applyTo: "**"`；也可合并进 `.github/copilot-instructions.md` |
| `openhands` | `.openhands/microagents/moo-app-security-audit.md` | microagent（name/agent/triggers） |
| `trae` | `.trae/rules/project_rules.md` | 单文件，追加带标记段落 |
| `junie` | `.junie/guidelines.md` | 单文件，追加带标记段落 |
| `aider` | `CONVENTIONS.md` | 单文件，追加带标记段落 |
| `warp` | `WARP.md` | 单文件，追加带标记段落 |

> 单文件目标（AGENTS.md / WARP.md / CONVENTIONS.md / project_rules.md / guidelines.md）采用
> `<!-- moo-app-security-audit:begin --> … :end -->` 标记追加，重复运行不会重复写入，删除整段即可卸载。

### 4. 组合

| target | 效果 |
|---|---|
| `all-project` | `AGENTS.md` + cursor + windsurf + cline + copilot + continue + kiro（忽略 `--dest`） |

## 三、手动步骤（不想用脚本时）

**QwenPaw**
```bash
cp -r "$SKILL_DIR" <workspace>/skills/
qwenpaw skills enable moo-app-security-audit --agent-id <agent-id>
qwenpaw skills test  moo-app-security-audit --agent-id <agent-id>   # 安全扫描
```

**Claude Code**
```bash
mkdir -p ~/.claude/skills && cp -r "$SKILL_DIR" ~/.claude/skills/
# 也可在项目根放 CLAUDE.md 指向 SKILL.md 绝对路径
```

**Codex CLI**
```bash
mkdir -p ~/.codex/prompts
sed "s#{{SKILL_DIR}}#$SKILL_DIR#g" "$SKILL_DIR/adapters/codex/prompt.md" \
  > ~/.codex/prompts/moo-app-security-audit.md
# 或在项目根 AGENTS.md 中引入本技能规则段
```

**Gemini CLI**
```bash
mkdir -p ~/.gemini/commands
# 见 adapters/rules/body.md，或直接 install.sh --target gemini-cli --apply
# 也可在 .gemini/settings.json 的 context 配置中包含 AGENTS.md
```

**Cursor / Windsurf / Cline / Roo / Kilo / Continue / Augment / Kiro / Copilot / OpenHands**
```bash
sh "$SKILL_DIR/scripts/install.sh" --target <cursor|windsurf|cline|roo|kilo|continue|augment|kiro|copilot|openhands> --apply
```

**Aider / Warp / Trae / Junie**
```bash
sh "$SKILL_DIR/scripts/install.sh" --target <aider|warp|trae|junie> --apply   # 单文件追加段落
```

## 四、安装后自检

```bash
python3 "$SKILL_DIR/scripts/fpk_audit.py"        # 无参数 = 打印用法（退出码 1 属正常）
sh "$SKILL_DIR/scripts/install.sh" --list        # 目标清单
# 接入自检（需有可访问的 Moo）
curl -s -H "X-Trim-Isadmin: true" -H "X-Moo-Admin: 1" \
  http://127.0.0.1:13813/app/moo/api/version     # {"trusted":true,...}
```

## 五、更新与卸载

| 形态 | 更新 | 卸载 |
|---|---|---|
| 技能目录（复制） | 重跑 `--apply` 覆盖 | 删 `<落地>/moo-app-security-audit` |
| 技能目录（`--link`） | 改本目录即生效 | 删符号链接 |
| 提示词/命令文件 | 重跑 `--apply` | 删对应 `.md`/`.toml` |
| 规则文件 | 重跑 `--apply`（覆盖） | 删对应文件 |
| 单文件段落 | 重跑自动跳过（幂等） | 删 `begin/end` 之间整段 |

## 六、平台无关约束

- **只读**：不安装/不运行/不改配置被审应用；临时产物落临时目录。
- **默认保留产物**：出报告后按「七、收尾」用 1/2/3 让用户决定，不自动删。
- **能力降级**：没有 python3 就用 shell + grep；没有 curl 用内置下载；没有面板会话就只做只读粗探并说明。
- **凭据不落盘**：报告只描述类型，不贴原文。
- **不假设路径**：各 Agent 版本迭代快，路径以你本地版本为准；脚本默认值对不上时用 `--dest`。

## 七、未覆盖的形态

| 平台 | 原因 / 替代方案 |
|---|---|
| Claude Desktop、ChatGPT 网页版 | 没有「读取本地文件」的技能位；需把脚本包成 **MCP Server** 才能调用（本技能暂未提供，可按 `scripts/fpk_audit.py` 包装） |
| Dify / Coze 等云端编排 | 无本地文件系统；只能把 SKILL.md 作为知识库 + 自行接 HTTP 工具 |
| 仅支持单一 system prompt 的 Agent | 用 `adapters/generic/PROMPT.md`，把 `{{SKILL_DIR}}` 换成实际路径后贴入 |

## 八、常见问题

- **Hermes/爱马仕 的目录在哪？** 在它的设置/文档里找「skills」或「prompts」目录，用 `--dest` 指过去即可。
- **规则没生效？** 确认运行目录是项目根；Cursor 需要 `.mdc` 且 `alwaysApply: true`；Windsurf 需 `trigger: always_on`；Kiro 需 `inclusion: always`（脚本已写好）。
- **文件被覆盖了吗？** 规则类按固定文件名写入并覆盖同名文件；单文件类只追加带标记段落，不动你的既有内容。
- **扫描器报警？** QwenPaw 自带的 skill scanner 已通过（0 findings）。其它平台若报「外联原语」，说明脚本确实会下载上游 FPK 做 sha 对比，属预期用途。


## Windows（PowerShell 5.1+）

```powershell
powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -List
powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Target claude-code -Apply
powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Target skills-dir -Dest D:\skills -Apply
```

参数与 `install.sh` 一致：`-Target` `-Dest` `-Dep` `-Link` `-Apply` `-List`；默认 dry-run。
复制时同样排除 `.git/`、`.pushpack*`、`__pycache__/`、`*.pyc`、`node_modules/`。
