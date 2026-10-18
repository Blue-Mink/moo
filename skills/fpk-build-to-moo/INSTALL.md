# 安装：fpk-build-to-moo（跨 Agent）

从构建 FPK 到发布 Moo 应用源。构建/打包依赖姊妹技能 **`fn-fpk-builder`**。

## 一键安装

```bash
S=<本技能目录>/scripts/install.sh
sh $S --list
sh $S --target claude-code --apply
sh $S --target codex --apply
sh $S --target agents-md --apply
sh $S --target skills-dir --dest <目录> --apply --dep fn-fpk-builder   # 连依赖一起装
```

## 依赖与随附资源

- **`fn-fpk-builder`**：用 `--dep fn-fpk-builder`（或 `--with-deps`）一并复制。
- **`bundled/`**：从仓库安装时自动附带 `tools/gen-moo-json`、`tools/gen-repo-moo-json.py`、
  `docs/Moo应用源协议.md`；`SKILL.md` 里的仓库相对链接在独立环境下按 `bundled/` 解析。
- `python3`：生成器（gen-repo-moo-json.py）与文档自检；`gen-moo-json` 是 Go 源码，需 `go build`。

## 自检与卸载

```bash
python3 -m unittest discover -s tests     # 4 个用例：结构 / 链接 / 协议 / 脱敏
```

卸载：删落地目录（连同 `bundled/`）与对应提示词/规则文件。

## Windows（PowerShell 5.1+）

```powershell
powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -List
powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Target claude-code -Apply
powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Target skills-dir -Dest D:\skills -Apply
```

参数与 `install.sh` 一致：`-Target` `-Dest` `-Dep` `-Link` `-Apply` `-List`；默认 dry-run。
复制时同样排除 `.git/`、`.pushpack*`、`__pycache__/`、`*.pyc`、`node_modules/`。
