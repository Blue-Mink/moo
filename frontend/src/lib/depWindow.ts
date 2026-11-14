/**
 * 自动停用时段（自动更新依赖占用 → 自动停用时段）的纯函数校验 —— 0.6.551。
 *
 * 规则单一权威在【后端】：internal/api/autoupdate_deps.go 的
 * validateAutoUpdateWindow（HH:MM 格式、开始≠结束、跨度 ≥ 60 分钟，
 * 常量 depWindowMinMinutes）。这里是它的前端镜像，只做两件事：
 *   1) 改完立刻给红字（以前要等点保存、后端 400 才知道）；
 *   2) 保存前拦住非法值，少一次往返。
 * 后端仍会再校验一次；**改规则时两处必须同改**（否则会出现「前端说合法、
 * 后端 400」或反之）。
 *
 * 跨零点：start > end 表示次日（如 22:00 → 02:00 = 4 小时），跨度按 +1440 取模。
 */

/** 最短时段（分钟）——与后端 depWindowMinMinutes 一致。 */
export const DEP_WINDOW_MIN_MINUTES = 60;

/** "HH:MM" → [时, 分]；非法返回 null（后端 parseHHMM 同义）。 */
export function parseHHMM(v: string): [number, number] | null {
  const m = /^(\d{2}):(\d{2})$/.exec((v || '').trim());
  if (!m) return null;
  const h = Number(m[1]);
  const mi = Number(m[2]);
  if (h > 23 || mi > 59) return null;
  return [h, mi];
}

/** 时段跨度（分钟，跨零点按 +1440 取模）；任一端非法返回 -1。 */
export function depWindowSpanMinutes(start: string, end: string): number {
  const s = parseHHMM(start);
  const e = parseHHMM(end);
  if (!s || !e) return -1;
  return ((e[0] * 60 + e[1]) - (s[0] * 60 + s[1]) + 1440) % 1440;
}

/**
 * 校验结果：'' = 合法；否则为给用户看的中文原因（与后端报错同口径）。
 * 注意：开始 == 结束在后端是「不得相同」，运行时 inAutoUpdateWindow 才把
 * 脏配置当全天 —— 保存路径一律按「不得相同」处理。
 */
export function depWindowIssue(start: string, end: string): string {
  const s = parseHHMM(start);
  const e = parseHHMM(end);
  if (!s || !e) return '时段格式应为 HH:MM（00:00–23:59）';
  if (s[0] * 60 + s[1] === e[0] * 60 + e[1]) return '开始与结束时间不能相同';
  const span = depWindowSpanMinutes(start, end);
  if (span < DEP_WINDOW_MIN_MINUTES) {
    return `时段不得短于 1 小时（当前 ${formatSpanMinutes(span)}）`;
  }
  return '';
}

/** 分钟数 → 「2 小时 30 分」/「2 小时」/「30 分」。 */
export function formatSpanMinutes(min: number): string {
  if (min < 0) return '—';
  const h = Math.floor(min / 60);
  const m = min % 60;
  if (h > 0 && m > 0) return `${h} 小时 ${m} 分`;
  if (h > 0) return `${h} 小时`;
  return `${m} 分`;
}
