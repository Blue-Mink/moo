/**
 * 2026-11-26 B-MIRROR-PROBE-VISIBILITY P1-2 / B-SRC-LISTSYNC 缺陷 1：
 * 「上次/下次」相对时间文本 —— 纯函数，无副作用，单测直接断言。
 *
 * 口径与后端一致：
 *  - next_probe = last_probe + interval_s（配置口径；门控生效时实际最慢 idle_gap_s）
 *  - last_list_sync_at = 上次清单抓取成功时刻；last_list_sync_added = 上轮新增数
 */

export interface ProbeTimingInput {
  last_probe?: string;
  next_probe?: string;
  idle_throttled?: boolean;
  idle_gap_s?: number;
}

export interface ListSyncInput {
  last_list_sync_at?: string;
  last_list_sync_added?: number;
}

const MIN = 60_000;

/** 「刚刚 / N 秒前 / N 分钟前 / N 小时前」 */
export const relPast = (at: number, now: number): string => {
  const d = Math.max(0, now - at);
  if (d < 30_000) return '刚刚';
  if (d < MIN) return `${Math.round(d / 1000)} 秒前`;
  if (d < 60 * MIN) return `${Math.round(d / MIN)} 分钟前`;
  return `${Math.round(d / (60 * MIN))} 小时前`;
};

/** 「即将 / N 秒后 / N 分钟后（已到期也算即将）」 */
export const relFuture = (at: number, now: number): string => {
  const d = at - now;
  if (d <= 30_000) return '即将';
  if (d < MIN) return `${Math.ceil(d / 1000)} 秒后`;
  return `${Math.round(d / MIN)} 分钟后`;
};

/**
 * 加速源测速的时序说明：
 * 「上次测速 2 分钟前 · 下次约 3 分钟后（当前不活跃，最长 15 分钟）」
 * 无 last_probe（从未测速）→ 返回空串（界面不渲染）。
 */
export const probeTimingText = (h?: ProbeTimingInput | null, now: number = Date.now()): string => {
  if (!h?.last_probe) return '';
  const last = new Date(h.last_probe).getTime();
  if (!Number.isFinite(last)) return '';
  let out = `上次测速 ${relPast(last, now)}`;
  const next = h.next_probe ? new Date(h.next_probe).getTime() : NaN;
  if (Number.isFinite(next)) out += ` · 下次约 ${relFuture(next, now)}`;
  if (h.idle_throttled) {
    const gapMin = Math.max(1, Math.round((h.idle_gap_s ?? 900) / 60));
    out += `（当前不活跃，最长 ${gapMin} 分钟）`;
  }
  return out;
};

/**
 * 源列表自动同步的痕迹：「上次同步 12 分钟前 · 新增 0」。
 * 从未跑过 → 空串（界面不渲染，不假装「在跑」）。
 */
export const listSyncText = (r?: ListSyncInput | null, now: number = Date.now()): string => {
  if (!r?.last_list_sync_at) return '';
  const at = new Date(r.last_list_sync_at).getTime();
  if (!Number.isFinite(at)) return '';
  return `上次同步 ${relPast(at, now)} · 新增 ${r.last_list_sync_added ?? 0}`;
};

/**
 * 手动测速点击的计划（2026-11-27）。
 *
 * `force` **恒为 true**：手动点「立即测速」永远要真发一次探测，不得因为
 * 「60 秒内刚测过」被挡回（旧行为直接 toast「N 秒前刚测过」并 return，
 * 用户明确点了却拿不到结果 = 观感「点了没反应」）。
 * 节流只能约束自动轮询，不能约束用户显式动作。
 *
 * `note` 只在「刚测过」时给一句补充说明，附在结果 toast 末尾。
 */
export interface ManualProbePlan {
  force: boolean;
  note: string;
}

export const manualProbePlan = (lastProbe?: string, now: number = Date.now()): ManualProbePlan => {
  const at = lastProbe ? new Date(lastProbe).getTime() : NaN;
  const age = Number.isFinite(at) ? now - at : Number.POSITIVE_INFINITY;
  const note =
    age >= 0 && age < 60_000 ? `（上次为 ${Math.max(1, Math.round(age / 1000))} 秒前）` : '';
  return { force: true, note };
};
