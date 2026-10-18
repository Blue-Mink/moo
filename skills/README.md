# skills/ — Moo 的 Agent 技能（跨 Agent 可装）

给 AI Agent 用的 Moo 技能：理解、诊断、安全操作 Moo 的 HTTP API，以及把 FPK 发布成 Moo 应用源。
**与平台无关**：纯 Markdown + 标准工具（node / curl / jq 可选 / python3），可装进 QwenPaw、Claude Code、
Qwen Code、Codex、Gemini CLI、Cursor、Windsurf、Cline、Copilot、Kiro、OpenHands、Hermes 等。

## 技能清单

| 技能 | 用途 | 可安装 |
|---|---|---|
| `skills/moo/` | 使用 / 诊断 Moo：四平面模型、87 个 API、功能→接口映射、安装与热替换、安全规则、排障 | ✅ |
| `skills/fpk-build-to-moo/` | 构建 FPK → 写 moo.json 索引 → 发布到 Moo：构建、验收、协议、生成器、发布卫生 | ✅（自动附带生成器与协议文档） |
| `skills/ai/` | Moo 应用内 AI 能力技能包（纪律/排障/去重/安全），资料型技能（无脚本） | ✅（资料型） |
| `skills/moo-app-security-audit/` | 第三方应用安全审核：病毒/木马/恶意脚本、数据破坏、权限面、弱默认口令、攻击面；三层分析 + `fpk_audit.py` | ✅ |

## 跨 Agent 安装

```bash
S=skills/install-cross-agent.sh
sh $S --list                                  # 查看目标
sh $S --target cursor --apply                 # 装到当前项目的 Cursor 规则
sh $S --target claude-code --apply            # 装到 ~/.claude/skills/
sh $S --target codex --apply                  # Codex 提示词
sh $S --target all-project --apply            # AGENTS.md + Cursor + Windsurf + Cline + Continue + Copilot + Kiro
sh $S --target skills-dir --dest /path --apply --skill moo   # 只装 moo
sh $S --target cursor --apply --skill moo-app-security-audit  # 只装审核技能
sh $S --target skills-dir --dest /path --apply --include-ai   # 额外附带 ai 资料包
```

| 形态 | 目标 |
|---|---|
| 技能目录 | `qwenpaw` `claude-code` `qwen-code` `agents-skills` `skills-dir` |
| 提示词/命令 | `codex` `gemini-cli` `hermes` `generic` |
| 项目规则 | `agents-md` `cursor` `windsurf` `cline` `roo` `kilo` `continue` `augment` `copilot` `kiro` `openhands` `trae` `junie` `aider` `warp` |
| 组合 | `all-project` |

默认 **dry-run**，`--apply` 才写入；`--link` 用软链；`--skill moo,fpk-build-to-moo` 选技能；
`--dep fn-fpk-builder`（或 `--with-deps`）连带复制依赖技能。
复制时自动排除 `.git/`、`.pushpack*`、`__pycache__/`。

> **每个技能也自带独立安装器**：`skills/<name>/scripts/install.sh`（Windows 另有 `install.ps1`）+ `INSTALL.md`
> （moo / fpk-build-to-moo / ai / moo-app-security-audit 都有），可脱离本仓库单独分发到任意项目。

## 依赖

| 依赖 | 用途 | 缺失时 |
|---|---|---|
| `node` ≥ 18 | `moo/scripts/moo-api.mjs`（API 客户端 / SSE） | 用 `curl` 直接调 API |
| `curl` | `moo/scripts/moo-health.sh`、手工验证 | 必需 |
| `jq`（可选） | 健康检查美化输出 | 回退原始 JSON |
| `python3` | `fpk-build-to-moo/tests/test_docs.py`、`moo-app-security-audit/scripts/*.py`、安装脚本路径解析 | 审核脚本必需，其余为测试/辅助 |

## 配置

- `MOO_API_BASE`：API 基址。经面板网关用 `http://<NAS_IP>:5666/app/moo/api`；本机调试直连用
  `http://127.0.0.1:38100/api`（**38101 是 pprof 调试端口，不是 API**）。
- 写操作需要可信通道（面板网关）+ 管理员；`X-Trim-Isadmin: true`、非 GET 另需 `X-Moo-Admin: 1`。
- 直连 TCP 通道恒不信任身份头，admin API 返回 403——这是设计，不是故障。

## Quick start

```bash
bash skills/moo/scripts/moo-health.sh http://<NAS_IP>:5666/app/moo/api
node skills/moo/scripts/moo-api.mjs GET /api/version --base http://<NAS_IP>:5666/app/moo/api
```

## 目录结构

```
skills/
├── README.md                     # 本文件
├── install-cross-agent.sh        # 跨技能调度器（转发给各技能自带安装器）
├── ci_check.py                   # CI 自检
├── moo/
│   ├── SKILL.md                  # 入口：四平面模型、默认工作流、硬规则
│   ├── INSTALL.md                # 跨 Agent 安装说明
│   ├── references/               # product-model / api-full / feature-map / install-deploy / safety / troubleshooting
│   └── scripts/                  # moo-api.mjs、moo-health.sh、install.sh、install.ps1
├── fpk-build-to-moo/
│   ├── SKILL.md                  # 构建 → 索引 → 发布 → 验收 全链路
│   ├── INSTALL.md
│   ├── references/               # fpk-build / publish-workflow
│   ├── scripts/                  # install.sh / install.ps1
│   └── tests/test_docs.py        # 文档自检（结构/链接/脱敏）
├── moo-app-security-audit/       # 第三方应用安全审核
│   ├── SKILL.md / INSTALL.md
│   ├── references/               # 评分模型 / 风险模式 / 默认口令 / 检查表 / 报告模板
│   ├── scripts/                  # fpk_audit.py、supply_chain_compare.py、安装器
│   └── adapters/                 # codex / hermes / generic / rules 模板
└── ai/                           # 应用内 AI 技能包（资料型）
    ├── SKILL.md / INSTALL.md
    ├── manifest.json             # 应用侧同步清单（逐文件 sha256）
    └── scripts/                  # rehash.py、install.sh、install.ps1
```

## 维护与脱敏

- `skills/moo/references/api-full.md` 需与 `docs/API.md` 保持一致（改动 API 时同步）。
- 本目录禁止出现真实内网地址、凭据、token、个人标识：只允许占位符（`<NAS_IP>`、`<PANEL_PASSWORD>`、`<KEY>`）
  与 RFC5737 示例网段（`192.0.2.x`）。提交前跑一次脱敏扫描：

```bash
grep -rnE 'ghp_[A-Za-z0-9]{20,}|Abc[0-9]{6}|192\.168\.[0-9]+\.[0-9]+|10\.0\.0\.[0-9]+' skills/
# 期望 0 命中
```

## 自检

```bash
python3 skills/ci_check.py          # 结构/链接/manifest/安装器 dry-run/测试/脱敏（CI 同款）
cd skills/fpk-build-to-moo && python3 -m unittest discover -s tests
python3 skills/ai/scripts/rehash.py --check    # ai 包哈希是否与 manifest 一致
```

CI：`.github/workflows/skills-check.yml`（push/PR 命中 `skills/**`、`tools/**`、协议文档时自动跑）。

Windows 安装：`powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Target <同上> -Apply`。

## 已知缺口 / 注意

- **端口**：API 默认 `38100`（`MOO_WEB_PORT` 可覆盖）；`38101` 是 pprof 调试端口，调 `/api/*` 会 404。
  旧版文档曾把 38101 写成 API 端口，已修正。
- **`skills/ai/` 是资料型技能**：Moo 应用内 AI 能力包（`manifest.json` + 文档 + 新增的 `SKILL.md`）。
  应用侧同步只按 `manifest.json.files` 列表拉取（且逐文件校验 sha256），因此 `SKILL.md` 不会被应用下载，
  只用于 Agent 侧识别与安装（已核实 `internal/skillsync` 的拉取逻辑）。
- **`fpk-build-to-moo` 的仓库相对引用**：`docs/Moo应用源协议.md`、`tools/gen-*` 在仓库内是相对路径；
  独立安装时由安装器复制到 `bundled/`，请按 `SKILL.md` 中「依赖与跨 Agent 安装」一节取用。
- **写操作**：装/卸/改设置属于写操作，技能要求先向用户确认；直连通道 admin API 恒 403 属设计。
- **Windows**：自带 `scripts/install.ps1`（PowerShell 5.1+），`-List` / `-Target <名称> -Apply` 与 sh 版目标一致；也可用 Git Bash / WSL 跑 sh 版。
- **`cmd/notifyfire` 的 detailURL 默认 38100**（测试工具，可用 `--detail-url`/`MOO_DETAIL_URL` 覆盖）——
  如改过 `MOO_WEB_PORT`，正式发通知前请显式传参。

