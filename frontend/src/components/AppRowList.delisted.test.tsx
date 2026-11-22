/**
 * P1-4 已下架 · 行列表（移动端标准模式）：角标位置。
 *
 * 2026-11-23 用户令：行列表的「已下架」角标必须与极光/标准卡一致钉在**右上角**，
 * 不能再挂在应用名后面（会把长名称挤成「全…」）。
 */
import { describe, expect, it } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
(globalThis as unknown as { window: unknown }).window = {
  fetch: () => Promise.reject(new Error('unit test: no network')),
  matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }),
};

const { default: AppRowList } = await import('./AppRowList');
import type { AppInfo } from '../api/client';

const noop = () => {};

const app = (over: Partial<AppInfo>): AppInfo =>
  ({
    key: 'global-radio@demo-src',
    appname: 'global-radio',
    display_name: '全球电台 Docker 应用（容器运行）',
    installed: true,
    installed_version: '1.5.4',
    latest_version: '1.5.4',
    has_update: false,
    platform: 'x86',
    release_url: '',
    release_notes: '',
    status: 'stopped',
    source: 'demo-src',
    ...over,
  }) as AppInfo;

const html = (a: AppInfo) =>
  renderToStaticMarkup(
    <AppRowList apps={[a]} onInstall={noop} onUpdate={noop} onDetail={noop} onOpenApp={noop} />,
  );

describe('行列表 · 已下架角标位置', () => {
  it('角标绝对定位在行右上角，且出现在应用名之前（不再挂在名字后面）', () => {
    const out = html(app({ blacklisted: true, blacklist_reason: 'policy' }));
    const badge = out.indexOf('data-testid="delist-badge"');
    const name = out.indexOf('全球电台 Docker 应用（容器运行）');
    expect(badge).toBeGreaterThanOrEqual(0);
    expect(name).toBeGreaterThanOrEqual(0);
    expect(badge).toBeLessThan(name);
    // 定位类：absolute + right-3 + top-3
    const badgeTag = out.slice(badge, out.indexOf('>', out.indexOf('已下架')) + 1);
    expect(badgeTag).toContain('absolute');
    expect(badgeTag).toContain('right-3');
    expect(badgeTag).toContain('top-3');
    // 动作列下移让位
    expect(out).toContain('pt-8');
  });

  it('命中时名称行不再渲染内联角标（名字不被挤断的根因已移除）', () => {
    const out = html(app({ blacklisted: true, blacklist_reason: 'policy' }));
    // 名称 span 与紧随的 Docker 标记之间不应夹角标
    const name = out.indexOf('全球电台 Docker 应用（容器运行）');
    const after = out.slice(name, name + 400);
    expect(after).not.toContain('delist-badge');
  });

  it('非下架行不渲染角标、也不加让位内边距', () => {
    const out = html(app({}));
    expect(out).not.toContain('data-testid="delist-badge"');
    expect(out).not.toContain('pt-8');
  });
});
