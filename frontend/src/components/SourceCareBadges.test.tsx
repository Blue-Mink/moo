/**
 * 应用源自动监测徽标：分因文案 + 窄屏第二行 + 原因截断（2026-11-26 CARE P1-2）。
 *
 * 对应 BACKLOG §B-SRC-CARE-DEADFEED 📱 窄屏显示约束的 1/2/3/4/5 条：
 * 宽屏 inline 不变、窄屏下沉第二行 text-[11px]、文案分因、原因截 40 字全文进
 * title、禁止 absolute 贴右上。
 */
import { describe, expect, it } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';

const { SourceCareInline, SourceCareSecondLine, careHint } = await import('./SourceCareBadges');
import type { SourceEntry } from '../api/client';

const src = (over: Partial<SourceEntry>): SourceEntry =>
  ({ id: 's1', name: 'galaxy-saturn', url: 'https://example.com/moo.json', app_count: 0, ...over }) as SourceEntry;

const inline = (o: Partial<SourceEntry>) => renderToStaticMarkup(<SourceCareInline src={src(o)} />);
const line = (o: Partial<SourceEntry>) => renderToStaticMarkup(<SourceCareSecondLine src={src(o)} />);

describe('自动监测徽标 · 分因', () => {
  it('失败态：写「刷新失败 + 原因」，绝不写「无应用」', () => {
    const s = { fail_streak: 3, last_error: '仓库不存在（HTTP 404）' };
    expect(inline(s)).toContain('连续 3 次刷新失败');
    expect(inline(s)).not.toContain('无应用');
    expect(line(s)).toContain('连续 3 次刷新失败');
    expect(line(s)).toContain('仓库不存在（HTTP 404）');
    expect(line(s)).not.toContain('无应用');
  });

  it('空源态：写「连续 N 次无应用 · 再 M 次自动关闭」，绝不写「刷新失败」', () => {
    const s = { empty_streak: 3 };
    expect(inline(s)).toContain('连续 3 次无应用');
    expect(line(s)).toContain('再 2 次自动关闭');
    expect(line(s)).not.toContain('刷新失败');
  });

  it('失败优先于空源（两计数并存时不混写）', () => {
    const html = line({ fail_streak: 2, empty_streak: 4, last_error: 'HTTP 410' });
    expect(html).toContain('刷新失败');
    expect(html).not.toContain('无应用');
  });

  it('策略停用：提示已自动停用 + 原因；用户手动停用不出提示', () => {
    const auto = { enabled: false, disabled_by: 'auto-care' as const, last_error: '仓库不存在（HTTP 404）' };
    expect(inline(auto)).toContain('已自动停用');
    expect(line(auto)).toContain('已自动停用');
    expect(line(auto)).toContain('HTTP 404');
    expect(inline({ enabled: false })).toBe('');
    expect(line({ enabled: false })).toBe('');
  });
});

describe('自动监测徽标 · 布局与截断', () => {
  it('宽屏 inline 在窄屏隐藏；第二行只在窄屏显示（不用 absolute）', () => {
    const a = inline({ fail_streak: 1 });
    const b = line({ fail_streak: 1 });
    expect(a).toContain('hidden');
    expect(a).toContain('sm:inline-flex');
    expect(b).toContain('sm:hidden');
    expect(b).toContain('text-[11px]');
    expect(a).not.toContain('absolute');
    expect(b).not.toContain('absolute');
  });

  it('原因在界面截 40 字（…收尾），全文保留在 title', () => {
    const long = '仓库不存在（HTTP 404）：raw 直连与 gh-proxy / jsDelivr 镜像均返回 404，源地址已失效需要更换';
    const html = line({ fail_streak: 4, last_error: long });
    const shown = html.slice(html.indexOf('⚠'), html.indexOf('</div>'));
    expect(shown).toContain('…');
    // 界面展示的原因部分（去掉前缀与省略号）不超过 40 字
    const reason = shown.replace('⚠ 连续 4 次刷新失败 · ', '').replace('…', '');
    expect(Array.from(reason).length).toBeLessThanOrEqual(40);
    expect(html).toContain(long); // title 全文
  });
});

describe('自动监测徽标 · 自愈冷却 heal-capped（2026-11-26 CARE P1-1b）', () => {
  const capped = { enabled: false, disabled_by: 'auto-care' as const, heal_capped: true, heal_count_30d: 2 };

  it('窄屏第二行写「已自动停用 · 30 天内自动恢复已达 2 次，请手动确认」', () => {
    expect(line(capped)).toContain('已自动停用');
    expect(line(capped)).toContain('⚠');
    expect(line(capped)).toContain('30 天内自动恢复已达 2 次，请手动确认');
    expect(inline(capped)).toContain('已自动停用');
    expect(line(capped)).toContain('data-care-tone="heal-capped"');
  });

  it('heal_count_30d 缺省按 2 次展示', () => {
    const html = line({ enabled: false, disabled_by: 'auto-care', heal_capped: true });
    expect(html).toContain('已达 2 次');
  });

  it('title 说明已停止自动恢复 / 每 24 小时只读复查一次 / 请手动开启', () => {
    const html = line(capped);
    expect(html).toContain('已自动恢复 2 次仍未稳定');
    expect(html).toContain('Moo 已停止自动恢复');
    expect(html).toContain('每 24 小时只做一次只读复查');
    expect(html).toContain('请在应用源页确认源是否仍有效后手动开启');
  });

  it('优先级高于普通自动停用（不再出现旧的「每 6 小时低频复查」）', () => {
    const html = line({ ...capped, last_error: '仓库不存在（HTTP 404）' });
    expect(html).toContain('30 天内自动恢复已达');
    expect(html).not.toContain('每 6 小时');
  });

  it('未达上限时仍是原「已自动停用」文案；手动停用恒为 null', () => {
    expect(line({ enabled: false, disabled_by: 'auto-care' })).not.toContain('30 天内');
    expect(line({ enabled: false, disabled_by: 'auto-care', heal_capped: true })).toContain('30 天内');
    expect(inline({ enabled: false })).toBe('');
    expect(line({ enabled: false, last_recovered_at: '2026-11-26T09:00:00Z' })).toBe('');
  });
});

// 时间可注入：所有相对时间断言都用固定 NOW，避免真实时钟漂移导致偶发失败
const NOW = Date.parse('2026-11-26T10:00:00Z');
const iso = (ms: number) => new Date(ms).toISOString();
const lineAt = (o: Partial<SourceEntry>, now: number) =>
  renderToStaticMarkup(<SourceCareSecondLine src={src(o)} now={now} />);
const inlineAt = (o: Partial<SourceEntry>, now: number) =>
  renderToStaticMarkup(<SourceCareInline src={src(o)} now={now} />);

describe('自动监测徽标 · 自愈恢复 recovered（24h 窗口）', () => {
  it('24h 内：inline「已自动恢复」，第二行带相对时间与现有应用数', () => {
    const s = { enabled: true, last_recovered_at: iso(NOW - 5 * 60_000), app_count: 12 };
    expect(careHint(src(s), NOW)?.tone).toBe('recovered');
    expect(inlineAt(s, NOW)).toContain('已自动恢复');
    expect(lineAt(s, NOW)).toContain('✓ 已于 5 分钟前自动恢复 · 现有 12 个应用');
    expect(lineAt(s, NOW)).toContain('data-care-tone="recovered"');
    expect(lineAt(s, NOW)).toContain('如再次异常会重新进入监测');
  });

  it('小时级相对时间：3 小时前', () => {
    expect(lineAt({ enabled: true, last_recovered_at: iso(NOW - 3 * 3600_000), app_count: 7 }, NOW)).toContain(
      '已于 3 小时前自动恢复 · 现有 7 个应用',
    );
  });

  it('窗口边界：23h59m 仍显示，整 24h 起不显示', () => {
    const just = { enabled: true, last_recovered_at: iso(NOW - (24 * 3600_000 - 60_000)) };
    expect(careHint(src(just), NOW)?.tone).toBe('recovered');
    expect(lineAt(just, NOW)).toContain('23 小时前');
    const out = { enabled: true, last_recovered_at: iso(NOW - 24 * 3600_000) };
    expect(careHint(src(out), NOW)).toBeNull();
    expect(lineAt(out, NOW)).toBe('');
    expect(inlineAt(out, NOW)).toBe('');
  });

  it('时间非法 / 为空 / 在未来：不崩且为 null（不显示）', () => {
    for (const bad of ['not-a-date', '', '2026-13-45T99:99:99Z', undefined]) {
      const s = { enabled: true, last_recovered_at: bad, app_count: 3 };
      expect(careHint(src(s), NOW)).toBeNull();
      expect(lineAt(s, NOW)).toBe('');
    }
    // 未来时间（时钟漂移）不显示「-3 分钟前」
    expect(careHint(src({ enabled: true, last_recovered_at: iso(NOW + 3 * 60_000) }), NOW)).toBeNull();
  });

  it('恢复后已开始空源计数时显示空源，不显示已恢复', () => {
    const html = line({ last_recovered_at: '2026-11-26T00:00:00Z', empty_streak: 2 });
    expect(html).toContain('连续 2 次无应用');
    expect(html).not.toContain('已自动恢复');
  });

  it('当前仍在失败时显示失败，绝不错报已恢复', () => {
    const s = { enabled: true, last_recovered_at: iso(NOW - 60_000), fail_streak: 2, last_error: 'HTTP 502' };
    const html = lineAt(s, NOW);
    expect(html).toContain('连续 2 次刷新失败');
    expect(html).not.toContain('已自动恢复');
  });

  it('停用态即使有 last_recovered_at 也走停用分支；手动停用仍为 null', () => {
    const off = { enabled: false, disabled_by: 'auto-care' as const, last_recovered_at: iso(NOW - 60_000) };
    expect(careHint(src(off), NOW)?.tone).toBe('auto-off');
    expect(careHint(src({ enabled: false, last_recovered_at: iso(NOW - 60_000) }), NOW)).toBeNull();
  });
});
