/**
 * P1-4 已下架 · 极简 / 极光 两种网格（DESIGN.md §3/§4）。
 *
 * 极简：图标去饱和 + 右下角红点 Ban 角标 + 状态红字「已下架」（不塞原因正文）；
 * 极光：同一条渐变做 grayscale 垫底 + 白底红字角标 + 卡底暗玻璃原因条，
 *       白色标题/状态保留原样（可读性优先）；未装命中卡同样保留、不隐藏。
 */
import { describe, expect, it } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
// client.ts 在模块作用域执行 `window.fetch.bind(window)`，node 环境无 window；
// 渲染型单测先补最小桩**再**动态 import（静态 import 会被提升到桩之前）。
(globalThis as unknown as { window: unknown }).window = {
  fetch: () => Promise.reject(new Error('unit test: no network')),
  matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }),
};

const { AuroraGrid, MinimalIconGrid } = await import('./ViewModeGrids');
import type { AppInfo } from '../api/client';

const noop = () => {};

const app = (over: Partial<AppInfo>): AppInfo =>
  ({
    key: 'clouddl@demo-src',
    appname: 'clouddl',
    display_name: '云端下载器',
    description: '支持多协议离线下载与远程管理。',
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

const delisted = (over: Partial<AppInfo> = {}) =>
  app({ blacklisted: true, blacklist_reason: 'malware', blacklist_notice: '内置脚本会执行未知程序', ...over });

const minimal = (a: AppInfo) => renderToStaticMarkup(<MinimalIconGrid apps={[a]} onDetail={noop} />);
const aurora = (a: AppInfo) => renderToStaticMarkup(<AuroraGrid apps={[a]} onDetail={noop} />);

describe('极简模式 · 已下架', () => {
  it('图标去饱和 + 红点 Ban 角标 + 状态「已下架」+ 状态小字保留版本', () => {
    const out = minimal(delisted({ installed: true, installed_version: '2.3.1' }));
    expect(out).toContain('data-testid="delist-ban"');
    expect(out).toContain('已下架');
    expect(out).toContain('grayscale opacity-55');
    expect(out).toContain('已安装 · v2.3.1');
  });

  it('未装命中卡不隐藏，状态小字 = 未安装 · v<latest>', () => {
    const out = minimal(delisted());
    expect(out).toContain('云端下载器');
    expect(out).toContain('未安装 · v2.3.1');
  });

  it('极简卡不塞原因正文（完整说明留给详情页）', () => {
    expect(minimal(delisted())).not.toContain('内置脚本会执行未知程序');
  });
});

describe('极光模式 · 已下架', () => {
  it('同一条渐变做 grayscale 垫底（不是整块死灰）+ 白底红字角标 + 暗玻璃原因条', () => {
    const out = aurora(delisted({ installed: true, installed_version: '2.3.1' }));
    expect(out).toContain('data-testid="delist-aurora-gray"');
    expect(out).toContain('grayscale brightness-[.72]');
    expect(out).toContain('data-testid="delist-badge"');
    expect(out).toContain('bg-white/95');
    expect(out).toContain('data-testid="delist-reason"');
    expect(out).toContain('检测到恶意行为');
    expect(out).toContain('内置脚本会执行未知程序');
    // 文字仍是白色（标题/状态不被灰度吃掉）
    expect(out).toContain('text-white');
  });

  it('未装命中卡同样保留，notice 为空回落缺省副行', () => {
    const out = aurora(delisted({ blacklist_notice: '' }));
    expect(out).toContain('云端下载器');
    expect(out).toContain('moo 已停止提供下载与更新');
  });
});
