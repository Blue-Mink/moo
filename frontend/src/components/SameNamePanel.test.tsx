// @vitest-environment jsdom
/**
 * D3（0.6.579）同名透明化 · 前端用例（只提示不合并）
 *
 * 锁住五条：
 *  1. 有变体 → 标准卡 / 移动行都渲染 `ci-badge`（文案「另有同名变体」）；
 *  2. 无变体 → 一律不渲染（旧后端 or 无变体的应用零变化）；
 *  3. 详情候选表默认折叠，点标题展开、再点收起；行点 → onOpenKey(key)；
 *  4. 窄屏不遮挡名称：角标挂在**版本行**（flex-wrap），不落在名称节点内，
 *     且带 `shrink-0`（名称仍是 truncate 单行，不被角标挤成「全…」）；
 *  5. D2「原项目源」提示：代表卡=已装+搬运且同组存在 upstream_repo 为空的
 *     真实卡才出；同组只有别的搬运卡 ⇒ 不出（防止把搬运卡当原项目源）。
 */
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act, type ReactElement } from 'react';
import type { AppInfo } from '../api/client';

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
// client.ts 在模块作用域执行 `window.fetch.bind(window)`：jsdom 无 fetch，先补桩再动态 import。
(globalThis as unknown as { window: Record<string, unknown> }).window.fetch = () =>
  Promise.reject(new Error('unit test: no network'));
(globalThis as unknown as { window: Record<string, unknown> }).window.matchMedia = () =>
  ({ matches: false, addEventListener() {}, removeEventListener() {} }) as unknown as MediaQueryList;

const { default: AppCard } = await import('./AppCard');
const { default: AppRowList } = await import('./AppRowList');
const { default: SameNamePanel, CI_BADGE } = await import('./SameNamePanel');

const noop = () => {};

const app = (over: Partial<AppInfo>): AppInfo =>
  ({
    key: '1Panel@fnos-official',
    appname: '1Panel',
    display_name: '1Panel 运维面板',
    installed: false,
    installed_version: '',
    latest_version: '1.0.12',
    has_update: false,
    platform: 'x86',
    release_url: '',
    release_notes: '',
    status: '',
    source: 'fnos-official',
    ...over,
  }) as AppInfo;

const variant = (over: Partial<AppInfo> = {}) => app({ same_name_ci_count: 1, ...over });

const realEntry = (over: Record<string, unknown> = {}) => ({
  key: '1panel@fnos-store',
  source: 'fnos-store',
  display_name: '1Panel',
  version: '1.10.34-lts',
  arch: 'x86',
  installed: false,
  is_rep: false,
  upstream_repo: '',
  ...over,
});

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

const render = (el: ReactElement): HTMLElement => {
  act(() => root.render(el));
  return container;
};

const card = (a: AppInfo) => render(<AppCard app={a} onInstall={noop} onUpdate={noop} onDetail={noop} />);
const row = (a: AppInfo) =>
  render(<AppRowList apps={[a]} onInstall={noop} onUpdate={noop} onDetail={noop} />);

describe('D3 角标 · 标准卡 / 移动行', () => {
  // 注意：card() / row() 共用同一个 root，必须先渲染再断言（不能把两次
  // 渲染的结果攒进数组后一起断言——后一次渲染会把前一次的 DOM 顶掉）。
  it('有变体：两张卡都出「另有同名变体」角标', () => {
    const check = (el: HTMLElement) => {
      const badge = el.querySelector('[data-testid="ci-badge"]');
      expect(badge).not.toBeNull();
      expect(badge!.textContent).toContain(CI_BADGE);
      expect(badge!.getAttribute('title')).toContain('1 张同名变体卡');
    };
    check(card(variant()));
    check(row(variant()));
  });

  it('无变体 / 旧后端缺字段：一律不渲染角标', () => {
    for (const a of [app({}), app({ same_name_ci_count: 0 })]) {
      expect(card(a).querySelector('[data-testid="ci-badge"]')).toBeNull();
      expect(row(a).querySelector('[data-testid="ci-badge"]')).toBeNull();
    }
  });

  it('窄屏不遮挡名称：角标在版本行、带 shrink-0，名称仍单行截断', () => {
    const check = (el: HTMLElement) => {
      const badge = el.querySelector('[data-testid="ci-badge"]')!;
      const name = el.querySelector('[data-testid="app-name"]')!;
      // 角标不在名称节点内 ⇒ 不会把名称挤成「全…」
      expect(name.contains(badge)).toBe(false);
      expect(name.textContent).toBe('1Panel 运维面板');
      expect(name.className).toContain('truncate');
      // 不抢宽：角标自身 shrink-0，父行允许换行（flex-wrap）
      expect(badge.className).toContain('shrink-0');
      expect(badge.parentElement!.className).toContain('flex-wrap');
    };
    check(card(variant()));
    check(row(variant()));
  });
});

describe('D3 详情候选表', () => {
  it('默认折叠；点标题展开；再点收起；点候选行回调 key', () => {
    const opened: string[] = [];
    const el = render(
      <SameNamePanel
        app={app({
          same_name: [
            { key: '1Panel@fnos-official', source: 'fnos-official', version: '1.0.12', installed: false, is_rep: true, upstream_repo: '' },
            realEntry() as never,
          ],
          same_name_ci: [{ key: '1panel@ZJB518', source: 'ZJB518', version: '2.0.14', installed: false, is_rep: true, case_variant: true, upstream_repo: 'hxido-rxm/fndepot' }],
          same_name_ci_count: 1,
        })}
        onOpenKey={(k) => opened.push(k)}
      />,
    );
    const toggle = el.querySelector('[data-testid="same-name-toggle"]') as HTMLButtonElement;
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    expect(el.querySelector('[data-testid="same-name-list"]')).toBeNull();

    act(() => { toggle.click(); });
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
    const rows = el.querySelectorAll('[data-testid="same-name-row"]');
    // same_name(2) ∪ same_name_ci(1)，互补不重复
    expect(rows.length).toBe(3);
    expect(el.textContent).toContain('大小写变体');
    expect(el.textContent).toContain('搬运 hxido-rxm/fndepot');
    expect(el.textContent).toContain('原项目源');
    expect(el.textContent).toContain('v1.10.34-lts');
    expect(el.textContent).toContain('v2.0.14');

    act(() => { (el.querySelectorAll('[data-testid="same-name-row"]')[2] as HTMLButtonElement).click(); });
    expect(opened).toEqual(['1panel@ZJB518']);

    act(() => { toggle.click(); });
    expect(el.querySelector('[data-testid="same-name-list"]')).toBeNull();
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
  });

  it('旧后端缺字段 / 只有自己一张：整块不渲染（静默）', () => {
    expect(render(<SameNamePanel app={app({})} onOpenKey={noop} />).querySelector('[data-testid="same-name-panel"]')).toBeNull();
    expect(
      render(
        <SameNamePanel
          app={app({ same_name: [{ key: '1Panel@fnos-official', source: 'fnos-official', installed: false, is_rep: true }] })}
          onOpenKey={noop}
        />,
      ).querySelector('[data-testid="same-name-panel"]'),
    ).toBeNull();
    expect(render(<SameNamePanel app={null} onOpenKey={noop} />).querySelector('[data-testid="same-name-panel"]')).toBeNull();
  });
});

describe('D2 原项目源提示', () => {
  const moverRep = (group: unknown[]) =>
    app({
      installed: true,
      installed_version: '2.0.14',
      upstream_repo: 'hxido-rxm/fndepot',
      source_is_fork: true,
      same_name: group as never,
    });

  it('已装+搬运 且同组有真实卡（upstream_repo 空）→ 出提示，写明来源与版本', () => {
    const el = render(<SameNamePanel app={moverRep([realEntry()])} onOpenKey={noop} />);
    const hint = el.querySelector('[data-testid="original-source-hint"]');
    expect(hint).not.toBeNull();
    expect(hint!.textContent).toContain('原项目源');
    expect(hint!.textContent).toContain('fnos-store');
    expect(hint!.textContent).toContain('v1.10.34-lts');
    expect(hint!.textContent).toContain('已装卡 v2.0.14');
  });

  it('同组只有别的搬运卡 / 本卡非搬运 / 未装 → 不出提示', () => {
    // 只有搬运卡：upstream_repo 非空 ⇒ 不是原项目源
    expect(
      render(<SameNamePanel app={moverRep([realEntry({ upstream_repo: 'other/fndepot' })])} onOpenKey={noop} />).querySelector(
        '[data-testid="original-source-hint"]',
      ),
    ).toBeNull();
    // 本卡非搬运
    expect(
      render(
        <SameNamePanel app={app({ installed: true, same_name: [realEntry() as never] })} onOpenKey={noop} />,
      ).querySelector('[data-testid="original-source-hint"]'),
    ).toBeNull();
    // 未装
    expect(
      render(
        <SameNamePanel
          app={app({ installed: false, upstream_repo: 'hxido-rxm/fndepot', same_name: [realEntry() as never] })}
          onOpenKey={noop}
        />,
      ).querySelector('[data-testid="original-source-hint"]'),
    ).toBeNull();
  });
});
