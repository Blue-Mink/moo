/**
 * 0.6.326z5 前端 2.1（推荐位禁用漏判）单测矩阵。
 *
 * 覆盖：命中卡可见 / 命中卡 user_hidden / 命中卡不在 apps / recommendedApps < 3 /
 * 有搜索词 / 有 activeCategory / 非 recommended tab，以及 FeaturedShowcase 的
 * featuredProp 分支（过滤后不足 3 位回落既有随机补足、不重复）。
 *
 * 证据构成：
 *  · 等价对拍——文件内保留**抽取前**的内联实现逐字副本（legacy*），在「无禁用命中」
 *    的输入上断言与抽取后结果逐项一致（证明零行为漂移）。
 *  · 反向验证——在「命中卡 user_hidden」的输入上断言 legacy 会把禁用卡钉回列表，
 *    而新实现不会（证明该修复点确有「修复前必失败」证据）。
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AppInfo } from '../api/client';
import {
  baseKeyOf,
  isHiddenApp,
  pickFeaturedApps,
  resolveDisplayApps,
  resolvePinnedRecommended,
} from './recoPins';

const mk = (key: string, over: Partial<AppInfo> = {}): AppInfo =>
  ({
    key,
    appname: key.split('@')[0],
    display_name: key,
    installed: false,
    installed_version: '',
    latest_version: '1.0.0',
    has_update: false,
    ...over,
  }) as AppInfo;

const keys = (l: AppInfo[]) => l.map((a) => a.key);

// ─────────────────────────── 抽取前的内联实现（逐字副本，仅作对拍基准）──────
// App.tsx:1452-1470（修复前）
function legacyDisplayApps(p: {
  apps: AppInfo[]; filteredApps: AppInfo[]; recommendedApps: AppInfo[];
  activeFilter: string; searchQuery: string; activeCategory?: string | null;
}): AppInfo[] {
  const { apps, filteredApps, recommendedApps, activeFilter, searchQuery, activeCategory } = p;
  if (activeFilter !== 'recommended' || recommendedApps.length < 3 || searchQuery.trim() || activeCategory) {
    return filteredApps;
  }
  const pinned: AppInfo[] = [];
  const baseKeys = new Set<string>();
  for (const rec of recommendedApps.slice(0, 3)) {
    const base = (rec.key || rec.appname || '').split('@')[0];
    const hit = apps.find((a) => (a.key || a.appname || '').split('@')[0] === base && base !== '');
    pinned.push(hit ?? rec);
    if (base) baseKeys.add(base);
  }
  const rest = filteredApps.filter((a) => {
    const b = (a.key || a.appname || '').split('@')[0];
    return !b || !baseKeys.has(b);
  });
  return [...pinned, ...rest];
}
// ─────────────────────────────────────────────────────────────────────────

describe('baseKeyOf / isHiddenApp', () => {
  it('base key = appname@源名 的 appname 段；缺字段回落 appname / 空串', () => {
    expect(baseKeyOf(mk('alpha@src1'))).toBe('alpha');
    expect(baseKeyOf({ key: '', appname: 'beta' } as AppInfo)).toBe('beta');
    expect(baseKeyOf({ key: '', appname: '' } as AppInfo)).toBe('');
    expect(baseKeyOf(undefined)).toBe('');
    expect(baseKeyOf(null)).toBe('');
  });
  it('user_hidden 严格 === true 才算禁用（undefined/false 不算）', () => {
    expect(isHiddenApp({ user_hidden: true })).toBe(true);
    expect(isHiddenApp({ user_hidden: false })).toBe(false);
    expect(isHiddenApp({})).toBe(false);
    expect(isHiddenApp(undefined)).toBe(false);
  });
});

describe('resolvePinnedRecommended — 钉位矩阵', () => {
  it('命中卡可见：按 recommendedApps 原序钉入真实卡片，baseKeys 记满 3 个', () => {
    const a = mk('fa@moo'), b = mk('fb@moo'), c = mk('fc@moo');
    const r = resolvePinnedRecommended([a, b, c], [mk('fa@snap'), mk('fb@snap'), mk('fc@snap')]);
    expect(keys(r.pinned)).toEqual(['fa@moo', 'fb@moo', 'fc@moo']);
    expect(r.pinned[0]).toBe(a); // 取的是 apps 里的真实对象，不是推荐条目
    expect([...r.baseKeys].sort()).toEqual(['fa', 'fb', 'fc']);
  });

  it('命中卡 user_hidden：该位整位跳过，但 base 仍记入 baseKeys（防从 rest 冒出）', () => {
    const hidden = mk('fa@moo', { user_hidden: true });
    const b = mk('fb@moo'), c = mk('fc@moo');
    const r = resolvePinnedRecommended([hidden, b, c], [mk('fa@s'), mk('fb@s'), mk('fc@s')]);
    expect(keys(r.pinned)).toEqual(['fb@moo', 'fc@moo']); // 2 位，不补足（由 rest 续上）
    expect(r.baseKeys.has('fa')).toBe(true); // 跳过位仍占 base
    expect(keys(r.pinned)).not.toContain('fa@moo');
  });

  it('命中卡不在 apps：回落推荐条目本身（appname@源名 快照同构）', () => {
    const snap = mk('gz@snapshot');
    const r = resolvePinnedRecommended([mk('other@moo')], [snap, mk('b@s'), mk('c@s')]);
    expect(r.pinned[0]).toBe(snap);
  });

  it('命中的禁用位全部跳过；无命中位回落快照条目；baseKeys 记满三位', () => {
    const apps = [mk('fa@moo', { user_hidden: true }), mk('fb@moo', { user_hidden: true })];
    const r = resolvePinnedRecommended(apps, [mk('fa@s'), mk('fb@s'), mk('fc@s')]);
    // fa/fb 命中禁用卡 → 整位跳过；fc 不在 apps → 回落推荐条目本身（快照非同构 hidden）
    expect(keys(r.pinned)).toEqual(['fc@s']);
    expect([...r.baseKeys].sort()).toEqual(['fa', 'fb', 'fc']);
  });

  it('清单任意条数：全部处理（0.6.379 去掉 slice(0,4)）', () => {
    const rec = ['a', 'b', 'c', 'd', 'e'].map((n) => mk(`${n}@s`));
    const apps = ['a', 'b', 'c', 'd', 'e'].map((n) => mk(`${n}@moo`));
    expect(keys(resolvePinnedRecommended(apps, rec).pinned)).toEqual(
      ['a@moo', 'b@moo', 'c@moo', 'd@moo', 'e@moo']
    );
    expect(keys(resolvePinnedRecommended([mk('fa@moo')], [mk('fa@s')]).pinned)).toEqual(['fa@moo']);
  });

  it('推荐条目 base 为空串：不查 apps、原样钉入、不记 baseKeys', () => {
    const rec = mk('', { key: '', appname: '' } as Partial<AppInfo>);
    const r = resolvePinnedRecommended([mk('fa@moo')], [rec, mk('fa@s'), mk('fb@s')]);
    expect(r.pinned[0]).toBe(rec);
    expect(r.baseKeys.has('')).toBe(false);
  });
});

describe('pickFeaturedApps — 横幅矩阵（0.6.379 清单驱动）', () => {
  afterEach(() => vi.restoreAllMocks());

  const pool = () => [mk('p1@moo'), mk('p2@moo'), mk('p3@moo', { user_hidden: true }), mk('p4@moo')];

  it('featuredProp 全可见：原序返回全部（不再截断到 3）', () => {
    const f = [mk('a@r'), mk('b@r'), mk('c@r'), mk('d@r')];
    expect(keys(pickFeaturedApps(pool(), f))).toEqual(['a@r', 'b@r', 'c@r', 'd@r']);
  });

  it('featuredProp 含 user_hidden：过滤掉该条，其余顺序不变（不补位）', () => {
    const f = [mk('a@r', { user_hidden: true }), mk('b@r'), mk('c@r'), mk('d@r')];
    expect(keys(pickFeaturedApps(pool(), f))).toEqual(['b@r', 'c@r', 'd@r']);
  });

  it('清单只有 2 条：只返回 2 条，不随机补位、不留空位', () => {
    vi.spyOn(Math, 'random').mockReturnValue(0);
    expect(keys(pickFeaturedApps(pool(), [mk('x@r'), mk('y@r')]))).toEqual(['x@r', 'y@r']);
  });

  it('空清单 / 全被禁用：返回空（不回落随机）', () => {
    expect(pickFeaturedApps(pool(), [])).toEqual([]);
    expect(keys(pickFeaturedApps(pool(), [mk('h1@r', { user_hidden: true })]))).toEqual([]);
  });

  it('清单 6 条：原序全部返回（布局层按条数分列）', () => {
    const f = ['a', 'b', 'c', 'd', 'e', 'f'].map((n) => mk(`${n}@r`));
    expect(keys(pickFeaturedApps(pool(), f))).toEqual(['a@r', 'b@r', 'c@r', 'd@r', 'e@r', 'f@r']);
  });

  it('featuredProp 未提供（undefined）：保留旧行为=全目录非禁用随机 3 款', () => {
    vi.spyOn(Math, 'random').mockReturnValue(0);
    expect(keys(pickFeaturedApps(pool(), undefined))).toEqual(['p1@moo', 'p2@moo', 'p4@moo']);
  });
});

describe('resolveDisplayApps — 发现页钉序 + 门控矩阵', () => {
  const base = () => {
    const apps = [mk('fa@moo'), mk('fb@moo'), mk('fc@moo'), mk('zz@moo'), mk('yy@moo')];
    const rec = [mk('fa@snap'), mk('fb@snap'), mk('fc@snap')];
    // filteredApps：与 App.tsx 同口径（推荐页已排 user_hidden）
    const filteredApps = apps.filter((a) => !a.user_hidden);
    return { apps, filteredApps, recommendedApps: rec };
  };
  const call = (over: Record<string, unknown> = {}) =>
    resolveDisplayApps({ ...base(), activeFilter: 'recommended', searchQuery: '', ...over } as never);

  it('非 recommended tab（all/installed/update_available）→ 原样返回 filteredApps', () => {
    for (const f of ['all', 'installed', 'update_available']) {
      const input = { ...base(), activeFilter: f, searchQuery: '' };
      expect(resolveDisplayApps(input as never)).toBe(input.filteredApps);
    }
  });

  it('recommendedApps 为空 → 不钉序，原样返回 filteredApps', () => {
    const input = { ...base(), activeFilter: 'recommended', searchQuery: '', recommendedApps: [] };
    expect(resolveDisplayApps(input as never)).toBe(input.filteredApps);
  });

  it('recommendedApps 只有 1 条 → 照样钉序（0.6.379 门控由 <3 放宽到 <1）', () => {
    const input = { ...base(), activeFilter: 'recommended', searchQuery: '', recommendedApps: [mk('fa@snap')] };
    expect(keys(resolveDisplayApps(input as never))).toEqual(['fa@moo', 'fb@moo', 'fc@moo', 'zz@moo', 'yy@moo']);
  });

  it('有搜索词 → 不钉序（纯空格不算：trim 后为空仍钉）', () => {
    const input = { ...base(), activeFilter: 'recommended', searchQuery: 'foo' };
    expect(resolveDisplayApps(input as never)).toBe(input.filteredApps);
    expect(keys(call({ searchQuery: '   ' })).slice(0, 3)).toEqual(['fa@moo', 'fb@moo', 'fc@moo']);
  });

  it('有 activeCategory → 不钉序', () => {
    const input = { ...base(), activeFilter: 'recommended', searchQuery: '', activeCategory: 'tools' };
    expect(resolveDisplayApps(input as never)).toBe(input.filteredApps);
  });

  it('正常钉序：前三在前（取值=apps 真实卡），其余列表剔除同 base', () => {
    const out = call();
    expect(keys(out)).toEqual(['fa@moo', 'fb@moo', 'fc@moo', 'zz@moo', 'yy@moo']);
  });

  it('命中卡 user_hidden：整位跳过且不从 rest 冒出（核心回归）', () => {
    const apps = [mk('fa@moo', { user_hidden: true }), mk('fb@moo'), mk('fc@moo'), mk('zz@moo')];
    const filteredApps = apps.filter((a) => !a.user_hidden); // 已排 fa
    const out = resolveDisplayApps({
      apps, filteredApps, recommendedApps: [mk('fa@snap'), mk('fb@snap'), mk('fc@snap')],
      activeFilter: 'recommended', searchQuery: '',
    } as never);
    expect(keys(out)).not.toContain('fa@moo');
    expect(keys(out)).toEqual(['fb@moo', 'fc@moo', 'zz@moo']);
  });

  it('命中卡 user_hidden 且同 base 别名卡可见：别名卡被 base 去重剔除（不冒充钉位）', () => {
    const apps = [mk('fa@moo', { user_hidden: true }), mk('fa@other'), mk('fb@moo'), mk('fc@moo')];
    const out = resolveDisplayApps({
      apps, filteredApps: apps.filter((a) => !a.user_hidden),
      recommendedApps: [mk('fa@snap'), mk('fb@snap'), mk('fc@snap')],
      activeFilter: 'recommended', searchQuery: '',
    } as never);
    expect(keys(out)).not.toContain('fa@other'); // 钉位已跳过 → 别名也不再冒出来
    expect(keys(out)).toEqual(['fb@moo', 'fc@moo']);
  });

  it('命中卡不在 apps：回落推荐快照条目（源缺失场景），其余不变', () => {
    const apps = [mk('zz@moo')];
    const snap = [mk('gz@snap'), mk('hx@snap'), mk('iy@snap')];
    const out = resolveDisplayApps({
      apps, filteredApps: apps, recommendedApps: snap,
      activeFilter: 'recommended', searchQuery: '',
    } as never);
    expect(keys(out).slice(0, 3)).toEqual(['gz@snap', 'hx@snap', 'iy@snap']);
    expect(keys(out)).toContain('zz@moo');
  });

  it('等价对拍：无禁用命中 → 与修复前逐项一致（含门控四态）', () => {
    const b = base();
    const cases: Record<string, unknown>[] = [
      { ...b, activeFilter: 'recommended', searchQuery: '' },
      { ...b, activeFilter: 'recommended', searchQuery: '   ' },
      { ...b, activeFilter: 'recommended', searchQuery: 'x' },
      { ...b, activeFilter: 'recommended', searchQuery: '', activeCategory: 'tools' },
      { ...b, activeFilter: 'all', searchQuery: '' },
      { ...b, activeFilter: 'installed', searchQuery: '' },
      { ...b, activeFilter: 'recommended', searchQuery: '', recommendedApps: [] },
    ];
    for (const c of cases) {
      expect(keys(resolveDisplayApps(c as never))).toEqual(keys(legacyDisplayApps(c as never)));
    }
  });

  it('反向验证（修复前必失败）：命中卡禁用 → 修复前被重新钉到列表首位', () => {
    const apps = [mk('fa@moo', { user_hidden: true }), mk('fb@moo'), mk('fc@moo'), mk('zz@moo')];
    const input = {
      apps, filteredApps: apps.filter((a) => !a.user_hidden),
      recommendedApps: [mk('fa@snap'), mk('fb@snap'), mk('fc@snap')],
      activeFilter: 'recommended', searchQuery: '',
    };
    const before = legacyDisplayApps(input as never);
    expect(keys(before)[0]).toBe('fa@moo'); // 修复前：禁用卡钉在发现页首位
    expect(keys(resolveDisplayApps(input as never))).not.toContain('fa@moo');
  });
});
