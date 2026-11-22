/**
 * P1-4 已下架 · 桌面标准卡（WebAppDetailCard，h-180 主列表卡）。
 * 真实浏览器 E2E 曾发现：桌面标准视图走的是这张卡，P1-4 首轮漏改 → 角标不出现。
 */
import { describe, expect, it } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
(globalThis as unknown as { window: unknown }).window = {
  fetch: () => Promise.reject(new Error('unit test: no network')),
  matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }),
};
const { default: WebAppDetailCard } = await import('./WebAppDetailCard');
import type { AppInfo } from '../api/client';

const noop = () => {};
const app = (over: Partial<AppInfo>): AppInfo => ({
  key: 'badapp@demo', appname: 'badapp', display_name: 'Bad App',
  installed: false, installed_version: '', latest_version: '1.0.0',
  has_update: false, source: 'demo', category: 'efficiency',
  ...over,
} as AppInfo);

const html = (a: AppInfo) => renderToStaticMarkup(
  <WebAppDetailCard app={a} onInstall={noop} onUpdate={noop} onDetail={noop} />,
);

describe('WebAppDetailCard delisted', () => {
  it('命中下架：角标 + 原因短句 + notice，未装卡只给「查看说明」', () => {
    const out = html(app({
      blacklisted: true, blacklist_reason: 'malware',
      blacklist_notice: '内置脚本会下载并执行未知程序', blacklist_since: '2026-11-20',
    }));
    expect(out).toContain('已下架');
    expect(out).toContain('检测到恶意行为');
    expect(out).toContain('内置脚本会下载并执行未知程序');
    expect(out).toContain('查看说明');
    expect(out).not.toContain('>安装<');
  });

  it('命中下架：未知原因码回落「已被停止分发」', () => {
    const out = html(app({ blacklisted: true, blacklist_reason: 'brand-new' }));
    expect(out).toContain('已被停止分发');
    expect(out).toContain('已下架');
  });

  it('未命中：照常渲染安装，且没有下架标记', () => {
    const out = html(app({}));
    expect(out).toContain('>安装<');
    expect(out).not.toContain('已下架');
  });
});
