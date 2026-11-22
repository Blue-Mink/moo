// @vitest-environment jsdom
/**
 * P1-1：手动更新队列载荷规整契约单测。
 *
 * 为什么必须有：后端 auto_update_skips.json 是**跨版本演进**的文件（字段
 * 增删、索引损坏、旧版本无 /api/updates/manual），前端若直接信任 JSON，
 * 一条脏数据就能让整个应用列表白屏。normalizeManualUpdates 负责把任何载荷
 * 收敛成 {count, items}，这里逐条钉住「丢脏、留好、计数自洽」。
 */
import { describe, expect, it } from 'vitest';
import { normalizeManualUpdates } from './client';

describe('normalizeManualUpdates', () => {
  it('保留完整条目并重算 count', () => {
    const got = normalizeManualUpdates({
      count: 99,
      items: [
        {
          app: 'trim.openclaw',
          version: '2026.8.35-6',
          reason: 'unknown',
          reason_label: '平台任务结果未知（可能仍在执行）',
          until: '2026-11-22T16:00:00+08:00',
        },
      ],
    });
    expect(got.count).toBe(1);
    expect(got.items[0]).toMatchObject({ app: 'trim.openclaw', reason: 'unknown' });
  });

  it('丢弃缺 app / 非对象的脏条目', () => {
    const got = normalizeManualUpdates({
      items: [null, 1, 'x', { reason: 'blocked' }, { app: '' }, { app: 'ok', reason: 'wizard' }],
    });
    expect(got.items.map(i => i.app)).toEqual(['ok']);
    expect(got.count).toBe(1);
  });

  it('reason 缺失时回落 unknown，可选字段缺失为 undefined', () => {
    const got = normalizeManualUpdates({ items: [{ app: 'a' }] });
    expect(got.items[0].reason).toBe('unknown');
    expect(got.items[0].until).toBeUndefined();
    expect(got.items[0].missing).toBeUndefined();
  });

  it('非对象 / 无 items 的载荷退化为空队列', () => {
    for (const raw of [null, undefined, 0, 'oops', {}, { items: 'nope' }]) {
      const got = normalizeManualUpdates(raw);
      expect(got).toEqual({ count: 0, items: [] });
    }
  });
});
