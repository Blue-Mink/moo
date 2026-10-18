# 安装：ai（Moo 应用内 AI 资料包，跨 Agent）

资料型技能：没有可执行脚本，装到 Agent 侧用于查阅 Moo 内置 AI 助手的纪律/排障/去重/安全口径。

## 一键安装

```bash
S=<本技能目录>/scripts/install.sh
sh $S --list
sh $S --target claude-code --apply
sh $S --target codex --apply
sh $S --target agents-md --apply
sh $S --target skills-dir --dest <目录> --apply
```

## 说明

- 本包同时被 Moo 应用内的 AI 同步使用：同步只按 `manifest.json.files` 拉取并逐文件校验 sha256，
  **`SKILL.md` 不在清单内**，因此不会被应用下载，只服务 Agent 侧识别。
- 修改任何被清单列出的文件后，必须重算 `manifest.json` 的 `files` 哈希与 `updated_at`，
  否则应用侧整包校验失败。
- 无网络调用、无凭据；示例使用占位符与 RFC5737 网段。

## 卸载

删落地目录与对应提示词/规则文件（单文件规则删 `begin/end` 段落）。

## Windows（PowerShell 5.1+）

```powershell
powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -List
powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Target claude-code -Apply
powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Target skills-dir -Dest D:\skills -Apply
```

参数与 `install.sh` 一致：`-Target` `-Dest` `-Dep` `-Link` `-Apply` `-List`；默认 dry-run。
复制时同样排除 `.git/`、`.pushpack*`、`__pycache__/`、`*.pyc`、`node_modules/`。
