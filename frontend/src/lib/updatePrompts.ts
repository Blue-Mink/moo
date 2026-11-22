/**
 * 批次①（2026-11-22 用户令）：更新类提示只在「有更新」页显示。
 *
 * 原本「上次自更新未生效」「N 个应用需手动处理」两块横幅在 全部 / 已安装 /
 * 有更新 三个 tab 都会渲染，用户要求「这些更新问题提示不要在每个页面都显示，
 * 只在有更新的页面显示」。收敛后不能让问题**静默丢失**：入口（侧栏 / 桌面 Dock /
 * 移动端 Dock）用小圆点 + title 计数兜底。
 *
 * 本文件是两个判定的唯一真源（纯函数、无 React 依赖，便于单测）。
 */

export type StoreFilterKey = 'all' | 'installed' | 'update_available' | 'recommended';

/** 圆点语义：red = 确有应用可更新；amber = 无应用可更新但有需人工处理的项。 */
export type UpdateBadgeKind = 'none' | 'red' | 'amber';

/** 横幅是否渲染：仅「有更新」页（发现页本来就没有这两块）。 */
export function shouldShowUpdatePrompts(filter: StoreFilterKey | string): boolean {
  return filter === 'update_available';
}

/** 计数兜底：NaN / 负数 / 非数字 一律按 0（后端字段脏了不能让角标变 NaN）。 */
function safeCount(n: number): number {
  if (typeof n !== 'number' || !Number.isFinite(n) || n <= 0) return 0;
  return Math.floor(n);
}

/** 需处理项数 = 手动更新队列条目数 + （自更新异常则 +1）。 */
export function updateIssueCount(manualCount: number, selfUpdateProblem: boolean): number {
  return safeCount(manualCount) + (selfUpdateProblem ? 1 : 0);
}

export interface UpdateBadgeInput {
  /** 可更新应用数（counts.update_available）。 */
  updateCount: number;
  /** 手动处理队列条目数（manualUpdates.items.length）。 */
  manualCount: number;
  /** 上次自更新 failed/unknown。 */
  selfUpdateProblem: boolean;
}

/** 圆点三态：有可更新应用 → red；否则有需处理项 → amber；都没有 → none。 */
export function updateBadgeKind(input: UpdateBadgeInput): UpdateBadgeKind {
  if (safeCount(input.updateCount) > 0) return 'red';
  if (updateIssueCount(input.manualCount, input.selfUpdateProblem) > 0) return 'amber';
  return 'none';
}
