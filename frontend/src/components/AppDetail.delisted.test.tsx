/**
 * P1-4 已下架 · 应用详情（DESIGN.md §5）。
 *
 * 命中清单的应用**不再 404**：详情正常打开，头部与内容之间插入红条
 *（标题 = 原因短句，正文 = 完整 notice + 「Moo 已停止提供…已装副本不会被远程卸载」，
 *  脚注 = 下架时间 + 来源）。
 *
 * 说明：整页由 Radix Dialog（Portal）承载，SSR 下 Portal 不产出节点，故此处
 * 直接对红条组件 + 详情页的两个纯判定（底板 / 主操作位）做断言：
 *   - 下架应用强制忽略 aurora（aBg=''，三视图进入完全一致，不套渐变）；
 *   - 未装下架应用主操作位 = blocked（「已停止分发」，不出现安装按钮）；
 *   - 已装下架应用主操作位 = open（保留「打开」，可继续用）。
 */
import { describe, expect, it } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
// client.ts 在模块作用域执行 `window.fetch.bind(window)`，node 环境无 window；
// 渲染型单测先补最小桩**再**动态 import（静态 import 会被提升到桩之前）。
(globalThis as unknown as { window: unknown }).window = {
  fetch: () => Promise.reject(new Error('unit test: no network')),
  matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }),
};

const { default: DelistBanner } = await import('./DelistBanner');
const { delistAuroraClass, delistPrimary } = await import('../lib/delist');
const { auroraFor } = await import('./ViewModeGrids');
import type { AppInfo } from '../api/client';

const app = (over: Partial<AppInfo>): AppInfo =>
  ({
    key: 'clouddl@demo-src',
    appname: 'clouddl',
    display_name: '云端下载器',
    installed: false,
    installed_version: '',
    latest_version: '2.3.1',
    has_update: false,
    platform: 'x86',
    release_url: '',
    release_notes: '',
    status: '',
    source: 'demo-src',
    ...over,
  }) as AppInfo;

const hitApp = (over: Partial<AppInfo> = {}) =>
  app({
    blacklisted: true,
    blacklist_reason: 'malware',
    blacklist_notice: '内置脚本会执行未知程序',
    blacklist_since: '2026-11-20',
    ...over,
  });

const banner = (a: AppInfo) => renderToStaticMarkup(<DelistBanner app={a} />);

describe('详情红条', () => {
  it('已装：标题=原因短句，正文=notice+说明，脚注=下架时间/来源', () => {
    const out = banner(hitApp({ installed: true }));
    expect(out).toContain('data-testid="delist-banner"');
    expect(out).toContain('已下架 · 检测到恶意行为');
    expect(out).toContain('内置脚本会执行未知程序');
    expect(out).toContain('Moo 已停止提供该应用的下载与更新；已安装的副本不会被远程卸载。');
    expect(out).toContain('下架时间 2026-11-20');
    expect(out).toContain('来源：远端下架黑名单');
  });

  it('未装：同样渲染红条（详情不再 404，红条解释为什么不能装）', () => {
    const out = banner(hitApp({ blacklist_reason: 'security', blacklist_notice: '内置服务可被未授权访问' }));
    expect(out).toContain('已下架 · 存在高危漏洞');
    expect(out).toContain('内置服务可被未授权访问');
  });

  it('notice 为空 → 正文回落「moo 已停止提供下载与更新」', () => {
    expect(banner(hitApp({ blacklist_notice: '' }))).toContain('moo 已停止提供下载与更新');
  });

  it('非下架应用不渲染红条', () => {
    expect(banner(app({}))).toBe('');
  });
});

describe('详情底板 · 下架强制忽略 aurora', () => {
  it('下架应用即使从极光视图进入也不套渐变（aBg 为空 → 白玻璃 + 红条）', () => {
    expect(delistAuroraClass(hitApp(), true, auroraFor)).toBe('');
  });

  it('非下架应用仍按名取极光色（回归保护）', () => {
    expect(delistAuroraClass(app({}), true, auroraFor)).toMatch(/^aurora-([1-9]|1[0-2])$/);
    expect(delistAuroraClass(app({}), false, auroraFor)).toBe('');
  });
});

describe('详情主操作位', () => {
  it('未装下架 → blocked（显示不可点「已停止分发」，不出现安装）', () => {
    expect(delistPrimary(hitApp({ installed: false }))).toBe('blocked');
  });

  it('已装下架 → open（保留「打开」，可继续用）', () => {
    expect(delistPrimary(hitApp({ installed: true }))).toBe('open');
  });

  it('非下架 → default（原有安装/打开逻辑）', () => {
    expect(delistPrimary(app({}))).toBe('default');
  });
});
