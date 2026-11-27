/**
 * 2026-11-27 盲点补测 4b（FIXPLAN 盲点 #2 前半）：「手动测速」结果的三态文案。
 *
 * 旧实现把结果压成一句「N 个正常，M 个失败」，于是三种完全不同的情况长得一样：
 *   1. 一次探测里**全部线路失败** —— 网络/出口问题，用户该去查网络或代理；
 *   2. 探测**有数据但 0 条成功**（全是限流 / 未测出）—— 源侧或链路受限，
 *      重试或换时间点更可能有用；
 *   3. **无数据**（清单为空，界面一行都没渲染）—— 根本不代表"测速失败"。
 * 三者都以「0 个正常」呈现时用户无法判断下一步做什么，故按可区分口径分开。
 *
 * 纯函数、无副作用，单测直接断言（见 mirrorProbeSummary.test.ts）。
 */
import type { MirrorHealth } from '../api/client';

export type ProbeOutcome = 'empty' | 'all-fail' | 'no-ok' | 'ok';

export interface ProbeSummary {
  ok: number;
  limited: number;
  fail: number;
  untested: number;
  total: number;
  outcome: ProbeOutcome;
  text: string;
}

/** 汇总一次探测的健康快照 → 可区分文案。 */
export const summarizeProbe = (h?: MirrorHealth | null): ProbeSummary => {
  const rows = h?.mirrors ?? [];
  let ok = 0;
  let limited = 0;
  let fail = 0;
  let untested = 0;
  for (const m of rows) {
    if (m.status === 'ok') ok += 1;
    else if (m.status === 'limited') limited += 1;
    else if (m.status === 'fail') fail += 1;
    else untested += 1; // '' = 未探测
  }
  const total = rows.length;
  const base = { ok, limited, fail, untested, total };

  if (total === 0) {
    return { ...base, outcome: 'empty', text: '暂无数据（未获取到加速源清单）' };
  }
  if (ok === 0 && fail === total) {
    return { ...base, outcome: 'all-fail', text: '探测失败：全部线路不可达' };
  }
  if (ok === 0) {
    const parts: string[] = [];
    if (limited > 0) parts.push(`${limited} 个限流`);
    if (untested > 0) parts.push(`${untested} 个未测出结果`);
    return {
      ...base,
      outcome: 'no-ok',
      text: `已探测，无成功线路${parts.length > 0 ? `（${parts.join('，')}）` : ''}`,
    };
  }
  // 有成功项：保持旧口径（ok/limited/fail），不改既有观感
  return {
    ...base,
    outcome: 'ok',
    text: `${ok} 个正常${limited > 0 ? `，${limited} 个限流` : ''}${fail > 0 ? `，${fail} 个失败` : ''}`,
  };
};
