/**
 * 应用源自动监测「自愈冷却」时间文案（2026-11-26 CARE P1-2b / P1-1b）。
 *
 * 服务端契约：heal_capped（30 天内自动恢复已达上限，保持停用等人工确认）、
 * heal_count_30d、last_recovered_at（RFC3339）。
 *
 * 设计要点：
 *  - **now 必须可注入**（默认 Date.now()），否则「X 分钟前」在单测里会漂移；
 *  - 非法 / 空 / 未来时间一律返回 null（界面静默不显示）→ 不崩、不出现
 *    「-3 分钟前」「Invalid Date」；
 *  - 只保留 24 小时窗口内的相对时间；超出窗口返回 null，由调用方决定不显示。
 */

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;

/** 恢复后展示「已自动恢复」的时间窗（用户口径：24 小时内）。 */
export const RECOVERED_WINDOW_MS = 24 * HOUR;

/** RFC3339 → 毫秒；空 / 非法返回 null（不抛错）。 */
export function parseCareTime(rfc3339?: string): number | null {
  if (!rfc3339) return null;
  const ms = Date.parse(rfc3339);
  return Number.isFinite(ms) ? ms : null;
}

/**
 * 相对时间：「刚刚 / N 分钟前 / N 小时前」。
 * 非法、未来时间（时钟漂移）、或距今 ≥24h → null。
 */
export function careAgo(rfc3339?: string, now: number = Date.now()): string | null {
  const at = parseCareTime(rfc3339);
  if (at === null) return null;
  const diff = now - at;
  if (!Number.isFinite(diff) || diff < 0) return null;
  if (diff >= RECOVERED_WINDOW_MS) return null;
  if (diff < MINUTE) return '刚刚';
  if (diff < HOUR) return `${Math.floor(diff / MINUTE)} 分钟前`;
  return `${Math.floor(diff / HOUR)} 小时前`;
}
