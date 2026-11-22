/**
 * P1-4 已下架 · 标准卡（A 版：灰卡 + 右上角「已下架」+ 卡下半部原因浮层）。
 *
 * 定稿（_prep_p14_blacklist_badge/DESIGN.md §1/§2）：
 *   - 命中清单的卡**不再从列表删除**（含已装卡）；
 *   - 已装 → 保留「打开」+「卸载」；未装 → 只给「查看说明」，不渲染安装；
 *   - 命中时不渲染安装 / 更新，并抑制「已忽略」等更新类徽章；
 *   - 副行优先 notice，没有就回落「moo 已停止提供下载与更新」。
 */
import { describe, expect, it } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
// client.ts 在模块作用域执行 `window.fetch.bind(window)`，node 环境无 window；
// 渲染型单测先补最小桩**再**动态 import（静态 import 会被提升到桩之前）。
(globalThis as unknown as { window: unknown }).window = {
  fetch: () => Promise.reject(new Error('unit test: no network')),
  matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }),
};

const { default: AppCard } = await import('./AppCard');
import type { AppInfo } from '../api/client';

const noop = () => {};

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

const html = (a: AppInfo) =>
  renderToStaticMarkup(
    <AppCard app={a} onInstall={noop} onUpdate={noop} onUninstall={noop} onDetail={noop} onOpenApp={noop} />,
  );

const hit = (over: Partial<AppInfo> = {}) =>
  html(app({ blacklisted: true, blacklist_reason: 'malware', blacklist_notice: '内置脚本会执行未知程序', blacklist_since: '2026-11-20', ...over }));

describe('标准卡 · 已下架', () => {
  it('已装：灰卡 + 角标 + 原因浮层，保留「打开」「卸载」，不渲染安装/更新', () => {
    const out = hit({ installed: true, installed_version: '2.3.1', has_update: true });
    expect(out).toContain('data-testid="delist-badge"');
    expect(out).toContain('已下架');
    expect(out).toContain('data-testid="delist-reason"');
    expect(out).toContain('检测到恶意行为');
    expect(out).toContain('内置脚本会执行未知程序');
    // 卡面去饱和
    expect(out).toContain('grayscale');
    // 已装：打开 + 卸载
    expect(out).toContain('打开 云端下载器');
    expect(out).toContain('>卸载</button>');
    // 不渲染安装 / 更新
    expect(out).not.toMatch(/安装<\/button>/);
    expect(out).not.toMatch(/更新<\/button>/);
  });

  it('未装：只给「查看说明」，卡片仍保留（不再从列表删除）', () => {
    const out = hit();
    expect(out).toContain('云端下载器');
    expect(out).toContain('查看说明');
    expect(out).not.toMatch(/安装<\/button>/);
    expect(out).not.toContain('卸载');
  });

  it('notice 为空 → 副行回落「moo 已停止提供下载与更新」', () => {
    const out = hit({ blacklist_notice: '' });
    expect(out).toContain('moo 已停止提供下载与更新');
  });

  it('未知原因码 → 短句回落「已被停止分发」（不瞎编、不显示原因码）', () => {
    const out = hit({ blacklist_reason: 'whatever-new-code' });
    expect(out).toContain('已被停止分发');
    expect(out).not.toContain('whatever-new-code');
  });

  it('命中时抑制「已忽略」更新类徽章', () => {
    const out = hit({ installed: true, update_ignored: true, has_update: true });
    expect(out).not.toContain('已忽略');
  });

  it('非下架卡行为不变：未装仍渲染安装按钮', () => {
    const out = html(app({ installed: false }));
    expect(out).toMatch(/安装<\/button>/);
    expect(out).not.toContain('data-testid="delist-badge"');
  });
});
