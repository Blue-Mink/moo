import { describe, it, expect } from 'vitest';
import { summarizeProbe } from './mirrorProbeSummary';
import type { MirrorHealth, MirrorStat } from '../api/client';

// 2026-11-27 盲点补测 4b（FIXPLAN 盲点 #2 前半）：「手动测速」三态文案。
// 反向验证：把 summarizeProbe 里 empty / all-fail / no-ok 三个分支删掉（都退化成
// 旧口径「0 个正常，N 个失败」）→ 本文件用例 1/2/3 与「互不相同」用例必红。

const stat = (key: string, status: MirrorStat['status'], err?: string): MirrorStat => ({
  key,
  label: key,
  latency_ms: status === 'ok' ? 120 : 0,
  speed_bps: 0,
  status,
  error: err,
  last_check: '2026-11-27T21:00:00+08:00',
  consec_fails: status === 'fail' ? 2 : 0,
});

const health = (mirrors: MirrorStat[]): MirrorHealth => ({
  mirrors,
  selected: 'auto',
  active: '',
  last_probe: '2026-11-27T21:00:00+08:00',
  interval_s: 300,
});

describe('summarizeProbe（手动测速结果三态文案）', () => {
  it('无数据（清单为空）≠ 测速失败', () => {
    const r = summarizeProbe(health([]));
    expect(r.outcome).toBe('empty');
    expect(r.total).toBe(0);
    expect(r.text).toBe('暂无数据（未获取到加速源清单）');
    expect(r.text).not.toContain('失败');
    // null/undefined 快照同口径（首屏还没拉到数据时组件可能传 null）
    expect(summarizeProbe(null).outcome).toBe('empty');
  });

  it('全部线路失败：明确说"探测失败"', () => {
    const r = summarizeProbe(health([
      stat('gh-proxy', 'fail', 'timeout'),
      stat('jsdelivr', 'fail', '502'),
    ]));
    expect(r.outcome).toBe('all-fail');
    expect(r.fail).toBe(2);
    expect(r.text).toBe('探测失败：全部线路不可达');
  });

  it('有数据但 0 成功（限流/未测出）：既不是失败也不是无数据', () => {
    const r = summarizeProbe(health([
      stat('gh-proxy', 'limited', '429'),
      stat('direct', 'fail', 'timeout'),
      stat('jsdelivr', ''),
    ]));
    expect(r.outcome).toBe('no-ok');
    expect(r.ok).toBe(0);
    expect(r.total).toBe(3);
    expect(r.text).toBe('已探测，无成功线路（1 个限流，1 个未测出结果）');
    expect(r.text).not.toBe('探测失败：全部线路不可达');
  });

  it('有成功项：保持旧口径（数量 + 限流/失败补充）', () => {
    const r = summarizeProbe(health([
      stat('gh-proxy', 'ok'),
      stat('jsdelivr', 'ok'),
      stat('direct', 'fail', 'timeout'),
    ]));
    expect(r.outcome).toBe('ok');
    expect(r.text).toBe('2 个正常，1 个失败');
    expect(summarizeProbe(health([stat('a', 'ok'), stat('b', 'limited')])).text).toBe('1 个正常，1 个限流');
  });

  it('三态文案两两不同（用户能据此判断下一步）', () => {
    const empty = summarizeProbe(health([])).text;
    const allFail = summarizeProbe(health([stat('a', 'fail')])).text;
    const noOk = summarizeProbe(health([stat('a', 'limited')])).text;
    expect(new Set([empty, allFail, noOk]).size).toBe(3);
  });
});
