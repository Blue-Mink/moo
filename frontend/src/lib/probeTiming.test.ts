import { describe, it, expect } from 'vitest';
import { probeTimingText, listSyncText, relPast, relFuture, manualProbePlan } from './probeTiming';

// 2026-11-26 B-MIRROR-PROBE-VISIBILITY P1-2 / B-SRC-LISTSYNC 缺陷 1：
// 「上次/下次 X 前 · 新增 N」文本口径。固定 now，断言确定性。
// 反向验证：把 idle_throttled 分支删掉 → 「当前不活跃，最长 15 分钟」用例必红。

const now = new Date('2026-11-26T10:00:00+08:00').getTime();
const at = (msAgo: number) => new Date(now - msAgo).toISOString();
const inFuture = (ms: number) => new Date(now + ms).toISOString();

describe('probeTiming（加速源测速时序文案）', () => {
  it('相对时间颗粒度：刚刚/秒/分钟/小时', () => {
    expect(relPast(now - 5_000, now)).toBe('刚刚');
    expect(relPast(now - 45_000, now)).toBe('45 秒前');
    expect(relPast(now - 2 * 60_000, now)).toBe('2 分钟前');
    expect(relPast(now - 3 * 3600_000, now)).toBe('3 小时前');
    expect(relFuture(now + 3 * 60_000, now)).toBe('3 分钟后');
    expect(relFuture(now - 60_000, now)).toBe('即将');
  });

  it('活跃：上次 + 下次（不含降频说明）', () => {
    const t = probeTimingText(
      { last_probe: at(2 * 60_000), next_probe: inFuture(3 * 60_000), idle_throttled: false },
      now,
    );
    expect(t).toBe('上次测速 2 分钟前 · 下次约 3 分钟后');
    expect(t).not.toContain('不活跃');
  });

  it('不活跃：显式标注最长拉长到 idle_gap_s', () => {
    const t = probeTimingText(
      { last_probe: at(7 * 60_000), next_probe: inFuture(8 * 60_000), idle_throttled: true, idle_gap_s: 900 },
      now,
    );
    expect(t).toBe('上次测速 7 分钟前 · 下次约 8 分钟后（当前不活跃，最长 15 分钟）');
  });

  it('无 last_probe / 脏值 → 空串（不假装有数据）', () => {
    expect(probeTimingText(null, now)).toBe('');
    expect(probeTimingText({}, now)).toBe('');
    expect(probeTimingText({ last_probe: 'not-a-date' }, now)).toBe('');
    expect(probeTimingText({ last_probe: at(60_000) }, now)).toBe('上次测速 1 分钟前');
  });
});

describe('listSyncText（源列表自动同步痕迹）', () => {
  it('展示「上次同步 X 前 · 新增 N」', () => {
    expect(listSyncText({ last_list_sync_at: at(12 * 60_000), last_list_sync_added: 0 }, now))
      .toBe('上次同步 12 分钟前 · 新增 0');
    expect(listSyncText({ last_list_sync_at: at(3 * 3600_000), last_list_sync_added: 2 }, now))
      .toBe('上次同步 3 小时前 · 新增 2');
  });

  it('从未同步 → 空串；added 缺省按 0', () => {
    expect(listSyncText(null, now)).toBe('');
    expect(listSyncText({}, now)).toBe('');
    expect(listSyncText({ last_list_sync_at: at(60_000) }, now)).toBe('上次同步 1 分钟前 · 新增 0');
  });
});

// 2026-11-27：手动测速不得被「60 秒内刚测过」挡回（用户令）。
// 反向验证：把 manualProbePlan 的 force 改回 `!(age < 60_000)` → 前两条必红。
describe('manualProbePlan（手动测速恒定强制真探测）', () => {
  it('10 秒前刚测过 → 仍然 force（旧行为这里会短路成不发请求）', () => {
    expect(manualProbePlan(at(10_000), now).force).toBe(true);
    expect(manualProbePlan(at(10_000), now).note).toBe('（上次为 10 秒前）');
  });

  it('59 秒 / 61 秒边界：force 恒 true，note 只在 60s 内出现', () => {
    expect(manualProbePlan(at(59_000), now)).toEqual({ force: true, note: '（上次为 59 秒前）' });
    expect(manualProbePlan(at(61_000), now)).toEqual({ force: true, note: '' });
  });

  it('从未测过 / 脏值 → force，无补充说明', () => {
    expect(manualProbePlan(undefined, now)).toEqual({ force: true, note: '' });
    expect(manualProbePlan('not-a-date', now)).toEqual({ force: true, note: '' });
  });

  it('未来时间（时钟回拨）不产生负数秒', () => {
    expect(manualProbePlan(inFuture(30_000), now).note).toBe('');
  });
});
