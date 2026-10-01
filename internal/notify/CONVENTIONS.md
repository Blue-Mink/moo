# 通知排版规范（2026-10-01 用户定稿）

**硬约束**：以后新增任何通知类别，一律按本规范排版。参考实现：
`source_sync_summary` / `source_sync_failed` / `mirror_switched`
（internal/api/notify_round.go、internal/api/mirrors.go，均走
`notifyEventRichV`）。

## 1. 结构：一律 `notifyEventRichV`，六种组合全量构造

| 渠道形式 | 简洁 concise | 友好 friendly（默认） | 完整 full |
|---|---|---|---|
| 卡片 card | 副题 ConciseSub + 行 RowsConcise | 副题 + 行 Rows（≤6） | 自动走 classic markdown 全量列表 |
| mdv2 表格 | ContentConcise | Table（≤20 行） | TableFull（无截断） |

- 渠道 Verbosity 由 `ForChannel` 自动选用变体；空字段回退主内容。
- 「完整」模式（企微）统一走 `sendWeComFull`：classic markdown 全量列表，
  不用卡片（卡片行平台限 6 行、mdv2 表格限 20 行，装不下全量）。

## 2. 各字段写法

- **Title**：单行摘要 = 类别名（如「源同步摘要」「加速源自动切换优选」），
  不带版本号/时间戳/emoji。
- **Content（友好）**：首行数字/结论摘要（如「本轮 156 个源成功 / 1 个失败」），
  空行后接 markdown 明细列表；列表条目超量按既有规则截断（Top 5 / 前 8）。
- **ContentConcise（简洁）**：只留关键数字 / 一条核心结论，1–2 行；
  与 ConciseSub、RowsConcise 各承载唯一信息，同一数字不重复出现。
- **ConciseSub**：简洁卡副题（防与卡片大字/两列行重复）。
- **ContentFull（完整）**：全部信息，不截断不隐藏（发送侧另有 4096 保护）。
- **Rows（卡片两列行）**：`Key` ≤5 字、`Value` ≤26 字；列表类用
  「名称 | 值」两列呈现。RowsConcise 只留最关键的 2–3 行。
- **Table / TableFull（mdv2）**：`| 项目 | 值 |` 或 `| 名称 | 版本 |`
  两列表格。

## 3. 统一措辞与符号

- 版本/状态变化：`旧 → 新`（箭头），带来源时用全角括号 `（源名）`。
- 切换类事件（源/镜像切换等）：友好/完整正文带 `原因：xxx` 独立行；
  卡片行含「切换原因」行。
- 标识符（镜像 key、应用 key 等）一律转可读标签再展示
  （`gh-proxy-hk → GH-Proxy HK`、`appname → display_name`）。
- 时间：md/mdv2 正文标题下 timeLine 事件时间行；卡片用 main_title.desc。

## 4. 尾部与截断（用户明确定稿）

- **消息尾部禁止「完整明细 + 链接」**（0.6.213 用户不要）。
- 企微 4096 字节上限：3800 字节按行截断，尾巴固定措辞
  「…（超出企业微信单条消息长度上限，其余条目见应用内通知记录）」（0.6.214）。
- 卡片形式的「打开通知详情」跳转按钮保留（= 用户接受的查看详情入口）。

## 5. 防刷屏

- 每类事件须有去重/冷却：notified map（按 应用×版本 / 指纹）或
  30 分钟冷却（切换类），防重启/重轮重复推。
- 企微机器人限流 20 条/分钟：多演示渠道同 webhook 时勿密集连发
  （一轮源同步会连发多事件，实测 45009 限流会丢消息）。

## 6. 新增事件 checklist

1. `notifyEventCatalog` 加事件 key（中文名 + 一句描述）；
2. pass 侧构造填齐 Variants 六字段（能省的回退，但六种组合都要过脑）；
3. 本地自测：临时加 5 个演示渠道（卡片/mdv2 × 简洁/友好/完整，同 webhook）
   触发一次，或载荷级捕获（临时渠道指向本机 HTTP 端点审真实 JSON）；
4. 部署测试机后验真实运行态：notify-log 的 channels 字段 errors=0；
5. 清理演示渠道；Release 更新日志一行记新类别。
