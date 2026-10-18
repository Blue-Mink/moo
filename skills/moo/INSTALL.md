# 安装：moo（跨 Agent）

Moo 操作/诊断技能。内容与平台无关；脚本需要 `node`（API 客户端）与 `curl`（健康检查）。

## 一键安装

```bash
S=<本技能目录>/scripts/install.sh
sh $S --list
sh $S --target claude-code --apply        # ~/.claude/skills/
sh $S --target codex --apply              # ~/.codex/prompts/
sh $S --target cursor --apply             # .cursor/rules/
sh $S --target agents-md --apply          # AGENTS.md
sh $S --target skills-dir --dest <目录> --apply
```

| 参数 | 说明 |
|---|---|
| `--target` | `qwenpaw` `claude-code` `qwen-code` `agents-skills` `skills-dir` / `codex` `gemini-cli` `hermes` `generic` / `agents-md` `cursor` `windsurf` `cline` `copilot` `kiro` / `all-project` |
| `--dest` | 自定义目录（`skills-dir`/`hermes`/`generic` 必填） |
| `--link` | 软链代替复制 |
| `--apply` | 真正写入（默认 dry-run） |

## 配置与依赖

- `MOO_API_BASE`：API 基址；面板网关 `http://<NAS_IP>:5666/app/moo/api`，本机 `http://127.0.0.1:38100/api`。
- 写操作需可信通道 + 管理员：`X-Trim-Isadmin: true`，非 GET 另需 `X-Moo-Admin: 1`。
- **38101 是 pprof 端口，不是 API**；直连 TCP 通道 admin API 恒 403（设计如此）。
- `jq` 可选（健康检查美化）；`node` ≥ 18 跑 `scripts/moo-api.mjs`。

## 自检与卸载

```bash
bash scripts/moo-health.sh http://127.0.0.1:38100/api
node scripts/moo-api.mjs GET /api/version --base http://127.0.0.1:38100/api
```

卸载：删落地目录 / 提示词文件 / 规则文件（单文件用 `begin/end` 段落整段删除）。

## Windows（PowerShell 5.1+）

```powershell
powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -List
powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Target claude-code -Apply
powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Target skills-dir -Dest D:\skills -Apply
```

参数与 `install.sh` 一致：`-Target` `-Dest` `-Dep` `-Link` `-Apply` `-List`；默认 dry-run。
复制时同样排除 `.git/`、`.pushpack*`、`__pycache__/`、`*.pyc`、`node_modules/`。
