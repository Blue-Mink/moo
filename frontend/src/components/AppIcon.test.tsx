/**
 * AppIcon 图标来源选择（0.6.326d 回归修复）单测。
 *
 * 回归：0.6.320 把 `iconUrl !== ''` 提到 useProxy 最外层后，`icon_url` 为空
 * 的社区源卡（全目录 1217/2156）直接落占位分支、从不请求后端 asset 代理。
 * 本用例锁定「外部源无论 icon_url 是否为空都走代理」这一契约。
 */
import { describe, expect, it } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import AppIcon from './AppIcon';
import type { AppInfo } from '../api/client';

const app = (over: Partial<AppInfo>): AppInfo =>
  ({ key: 'demo@src', display_name: 'Demo', name: 'demo', ...over }) as AppInfo;

const html = (a: AppInfo) => renderToStaticMarkup(<AppIcon app={a} className="w-14 h-14" />);
const src = (a: AppInfo) => {
  const m = html(a).match(/src="([^"]*)"/);
  return m ? m[1].replace(/&amp;/g, '&') : '';
};

describe('AppIcon 图标来源', () => {
  it('外部源 + icon_url 为空 → 走后端代理（回归用例：修复前渲染占位）', () => {
    const a = app({ source: 'seesky100825', icon_url: '', latest_version: '1.2.3' });
    expect(src(a)).toBe('/api/apps/demo%40src/asset?type=icon&v=1.2.3');
    expect(html(a)).toContain('<img');
  });

  it('外部源 + 相对路径 icon_url → 走后端代理', () => {
    const a = app({ source: 'seesky100825', icon_url: './demo/ICON.PNG' });
    expect(src(a)).toContain('/asset?type=icon');
  });

  it('外部源 + http 绝对直链 → 仍走后端代理（社区外链经镜像/缓存）', () => {
    const a = app({ source: 'seesky100825', icon_url: 'https://example.com/i.png' });
    expect(src(a)).toContain('/asset?type=icon');
  });

  it('内置目录源 + http 绝对直链 → 浏览器直载，不走代理（0.6.320 意图保留）', () => {
    const a = app({ source: 'fnos-apps', icon_url: 'https://cdn.example.com/i.png' });
    expect(src(a)).toBe('https://cdn.example.com/i.png');
  });

  it('应用中心安装应用（无源条目）+ 面板相对路径 → 走后端代理（0.6.320 修复保留）', () => {
    const a = app({ source: undefined, icon_url: '/app-center-static/icon/demo/icon.png' });
    expect(src(a)).toContain('/asset?type=icon');
  });

  it('应用中心安装应用（无源条目）+ icon_url 为空 → 占位（不请求代理）', () => {
    const a = app({ source: undefined, icon_url: '' });
    expect(html(a)).not.toContain('<img');
    expect(html(a)).toContain('squircle');
  });

  it('已装但无目录条目时版本戳回落 installed_version', () => {
    const a = app({ source: 'x', icon_url: '', installed_version: '0.1.0' });
    expect(src(a)).toContain('&v=0.1.0');
  });

  it('key 内特殊字符（@）被 URL 编码', () => {
    const a = app({ key: 'Nginx@Hxido-RXM', source: 'Hxido-RXM', icon_url: '' });
    expect(src(a)).toContain('/api/apps/Nginx%40Hxido-RXM/asset');
  });

  it('占位改首字母色块（0.6.339）：同一应用色相稳定，且不再渲染灰框', () => {
    const a = app({ source: undefined, icon_url: '', display_name: 'Navidrome' });
    const out = html(a);
    expect(out).toContain('>N</text>');
    expect(out).toContain('hsl(');
    expect(html(a)).toBe(out); // 纯函数：两次渲染同色
  });
});
