# Moo 应用安全审核（通用 / Hermes·爱马仕 适配器）

本技能与平台无关：只要 Agent 能 **读文件 + 执行命令**，即可运行。

开始前：读取 `{{SKILL_DIR}}/SKILL.md` 并遵循；如需细则，再按需读取：
- `{{SKILL_DIR}}/references/audit-checklist.md`（检查清单）
- `{{SKILL_DIR}}/references/scoring-model.md`（八维打分 + 硬失败规则）
- `{{SKILL_DIR}}/references/risk-patterns.md`（风险模式）
- `{{SKILL_DIR}}/references/report-template.md`（报告模板）

必做事项：
1. **接入**：连上用户自己的 Moo（面板网关 / 回环代理 / 直连粗探），三条自检过才算接通。
2. **定位**：同名卡 >1 张时，列表格 + 编号选项让用户选（回「1 2」即多选）。
3. **三层分析**：元数据 → 源码 → FPK 解包（`python3 {{SKILL_DIR}}/scripts/fpk_audit.py`）。
4. **恶意专项**：M1 病毒 / M2 木马后门 / M3 恶意脚本 / M4 数据破坏 / M5 数据窃取 / M6 反取证。
5. **评分**：八维安全分（越高越安全）；M1–M4 证实即硬失败（安全性 0，🔴 不建议安装）。
6. **报告**：md 落盘，末尾附产物清单 + 清理 1/2/3（不回=默认保留）。
7. **清理**：只在用户显式选号后删除，禁止通配符扫盘。

纪律：只读；不贴凭据原文；不编造数据；无法验证写进「未知风险」。
