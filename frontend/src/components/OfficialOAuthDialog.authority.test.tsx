// @vitest-environment jsdom
/**
 * 2026-11-26 加固 C 前端一侧：官方源状态按身份裁剪后的只读形态。
 *
 * 后端 GET /api/official/status 现在恒 200，但非管理员拿不到 panel_base /
 * last_error，并收到 can_admin=false。前端必须同步收写入口，否则用户点了
 * 授权按钮只会吃 403（体验上比「看得见的只读」差得多）。
 *
 * 三态口径：can_admin === false → 只读；true / undefined（旧后端）→ 行为不变。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';

// React 19 的 act() 需要环境标记，否则每次 setState 都打警告（jsdom 不自带）。
(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const statusBox: { current: Record<string, unknown> } = { current: { authorized: false } };

vi.mock('../api/client', async () => {
  const actual = await vi.importActual<typeof import('../api/client')>('../api/client');
  const noop = async () => undefined;
  return {
    ...actual,
    fetchOfficialStatus: async () => statusBox.current,
    officialAuthorize: noop,
    officialCallback: noop,
    officialCancel: noop,
    officialLogout: noop,
    officialAuthorizeHeadless: noop,
    fetchOfficialApps: async () => ({ total: 0, list: [] }),
  };
});

const { OfficialOAuthDialog, officialWriteAllowed } = await import('./OfficialOAuthDialog');

let container: HTMLDivElement | null = null;
let root: Root | null = null;

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => { root?.unmount(); });
  container?.remove();
  container = null;
  root = null;
  document.body.innerHTML = '';
});

/** 渲染一次对话框并等状态请求落地（Radix 对话框 portal 到 body，断言看 body）。 */
async function renderDialog(status: Record<string, unknown>) {
  statusBox.current = status;
  await act(async () => {
    root!.render(<OfficialOAuthDialog open onOpenChange={() => {}} />);
  });
  // 让 refreshStatus 的 promise 链 + setState 全部落地
  await act(async () => { await Promise.resolve(); });
}

const bodyText = () => document.body.textContent || '';
const hasTestId = (id: string) => !!document.querySelector(`[data-testid="${id}"]`);
/** 只取按钮文案：提示文案里本来就会提到「临时登录面板」等词，不能拿整段 textContent 断言。 */
const buttonTexts = () => Array.from(document.querySelectorAll('button')).map((b) => (b.textContent || '').trim());
const hasButton = (label: string) => buttonTexts().some((t) => t.includes(label));

describe('officialWriteAllowed（can_admin 三态）', () => {
  it('can_admin=false → 不可写', () => {
    expect(officialWriteAllowed({ authorized: true, can_admin: false })).toBe(false);
  });

  it('can_admin=true → 可写', () => {
    expect(officialWriteAllowed({ authorized: true, can_admin: true })).toBe(true);
  });

  it('can_admin 缺省（旧后端）→ 可写，行为零变化', () => {
    expect(officialWriteAllowed({ authorized: true })).toBe(true);
    expect(officialWriteAllowed(null)).toBe(true);
    expect(officialWriteAllowed(undefined)).toBe(true);
  });
});

describe('对话框 · 非管理员只读', () => {
  it('can_admin=false：给出「仅管理员」提示，且不渲染任何写操作入口', async () => {
    await renderDialog({ authorized: false, can_admin: false, ui_known: true, ui_supported: true });
    expect(hasTestId('official-admin-only')).toBe(true);
    expect(bodyText()).toContain('仅管理员可连接或授权官方源');
    expect(hasTestId('official-readonly-hint')).toBe(true);
    // 写操作入口（发起授权 / 新标签页授权 / 临时登录面板 / 断开连接）全部不渲染
    expect(hasButton('OAuth授权登录')).toBe(false);
    expect(hasButton('新标签页授权')).toBe(false);
    expect(hasButton('临时登录面板')).toBe(false);
    expect(hasButton('断开连接')).toBe(false);
  });

  it('can_admin=false：只读信息照常显示（已连接状态与有效期）', async () => {
    const expires = Date.now() + 3600_000;
    await renderDialog({ authorized: true, can_admin: false, expires_at: expires });
    expect(bodyText()).toContain('已连接');
    expect(bodyText()).toContain('令牌有效');
    expect(hasButton('断开连接')).toBe(false);
  });

  it('can_admin=true：正常渲染授权入口（未连接）', async () => {
    await renderDialog({ authorized: false, can_admin: true, ui_known: true, ui_supported: true });
    expect(hasTestId('official-admin-only')).toBe(false);
    expect(hasButton('OAuth授权登录')).toBe(true);
    expect(hasButton('新标签页授权')).toBe(true);
  });

  it('can_admin 缺省（旧后端）：行为与改造前完全一致（仍可发起授权 / 断开）', async () => {
    await renderDialog({ authorized: true });
    expect(hasTestId('official-admin-only')).toBe(false);
    expect(hasButton('断开连接')).toBe(true);
  });
});
