# Moo 应用安全审核（规则）

当用户要求审核 Moo 应用（或第三方 FPK）安全性时，按以下规则执行；完整细则读取
`{{SKILL_DIR}}/SKILL.md`，检查清单 `references/audit-checklist.md`，评分口径
`references/scoring-model.md`。

1. **接入**：连用户自己的 Moo（面板网关 `/app/moo/api`、回环 `127.0.0.1:13813/app/moo/api` 需
   `X-Trim-Isadmin: true` + `X-Moo-Admin: 1`、或直连端口粗探）。三条自检（about / apps / version trusted=true）
   过了才继续。
2. **定位**：同名卡 >1 张时列表格 + **编号选项 1/2/3**，用户回序号即可（可多选）。
3. **三层分析**：元数据 → 源码（maintainer_url/upstream_repo）→ FPK 解包
   （`python3 {{SKILL_DIR}}/scripts/fpk_audit.py <fpk>`）。
4. **恶意专项 M1–M6**：病毒·蠕虫 / 木马·后门 / 恶意脚本 / **数据删除·篡改·加密** / 数据窃取 / 反取证。
   **M1/M2/M4 证实 → 安全性 0、🔴 立即卸载**；M3/M5 → ≤10；无法判断只记入未知风险。
5. **评分**：八维安全分（越高越安全），综合 = min×0.6 + avg×0.4。
6. **报告**：md 落盘；末尾附「审计产物与清理确认」——1 全部保留（默认）/ 2 只留源码+报告 / 3 全部清理。
7. **清理**：只在用户回号后删；删除前列绝对路径与大小，删后验证；禁止 `rm -rf /tmp/*` 式扫盘。

纪律：**只读**（不安装/不运行被审应用）；不贴凭据原文；不编造结论；无法验证写进「未知风险」。
