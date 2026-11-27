// @vitest-environment jsdom
/**
 * 2026-11-26（加固 B2）前端：官方源「加密保存会话凭据」开关（opt-in，默认关）。
 *
 * 锁住四条：
 *  1. 默认关（开关未选中），且不展示确认面板；
 *  2. 开启必须过确认面板——面板里列出三条代价，点「确认开启」才真正调接口；
 *  3. encryption_available=false → 开关禁用 + 明确提示「本机密钥不可用」；
 *  4. can_admin=false（非管理员）→ 整块高级区不渲染（后端也会 403）。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const statusBox: { current: Record<string, unknown> } = { current: { authorized: false } };
const spy = vi.hoisted(() => ({ calls: [] as boolean[] }));

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
    setOfficialSessionEncryption: async (enabled: boolean) => {
      spy.calls.push(enabled);
      statusBox.current = { ...statusBox.current, session_encrypted: enabled, session_encryption_on: enabled };
      return { ok: true, encrypted: enabled };
    },
  };
});

const { OfficialOAuthDialog } = await import('./OfficialOAuthDialog');

let container: HTMLDivElement | null = null;
let root: Root | null = null;

beforeEach(() => {
  spy.calls = [];
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

async function renderDialog(status: Record<string, unknown>) {
  statusBox.current = status;
  await act(async () => {
    root!.render(<OfficialOAuthDialog open onOpenChange={() => {}} />);
  });
  await act(async () => { await Promise.resolve(); });
}

const switchEl = () => document.querySelector('[data-testid="official-session-encrypt-switch"]') as HTMLElement | null;
const hasTestId = (id: string) => !!document.querySelector(`[data-testid="${id}"]`);
const bodyText = () => document.body.textContent || '';
const buttonTexts = () => Array.from(document.querySelectorAll('button')).map((b) => (b.textContent || '').trim());
const hasButton = (label: string) => buttonTexts().some((t) => t.includes(label));

describe('官方源 · 加密保存会话凭据（opt-in）', () => {
  it('默认关：开关未选中，且不展示确认面板', async () => {
    await renderDialog({ authorized: false, can_admin: true, ui_known: true, ui_supported: true, encryption_available: true });
    expect(hasTestId('official-session-encryption')).toBe(true);
    const el = switchEl();
    expect(el).not.toBeNull();
    expect(el!.getAttribute('aria-checked')).toBe('false');
    expect(el!.hasAttribute('disabled')).toBe(false);
    expect(hasTestId('official-encrypt-confirm')).toBe(false);
    expect(spy.calls.length).toBe(0);
  });

  it('开启需确认：点开关只弹确认面板（列出三条代价），不直接调接口', async () => {
    await renderDialog({ authorized: false, can_admin: true, ui_known: true, ui_supported: true, encryption_available: true });
    await act(async () => { switchEl()!.click(); });
    await act(async () => { await Promise.resolve(); });
    expect(hasTestId('official-encrypt-confirm')).toBe(true);
    expect(bodyText()).toContain('降级到旧版本');
    expect(bodyText()).toContain('备份仍可解密');
    expect(bodyText()).toContain('随时可以关闭');
    // 未确认 → 没有发请求
    expect(spy.calls.length).toBe(0);
    // 确认后才真正切换
    await act(async () => {
      Array.from(document.querySelectorAll('button'))
        .find((b) => (b.textContent || '').includes('确认开启'))!
        .click();
    });
    await act(async () => { await Promise.resolve(); });
    expect(spy.calls).toEqual([true]);
    expect(hasTestId('official-encrypt-confirm')).toBe(false);
    expect(switchEl()!.getAttribute('aria-checked')).toBe('true');
    expect(hasTestId('official-encrypt-on')).toBe(true);
  });

  it('密钥不可用：开关禁用 + 提示「本机密钥不可用」，点了也不弹确认', async () => {
    await renderDialog({ authorized: false, can_admin: true, ui_known: true, ui_supported: true, encryption_available: false });
    expect(hasTestId('official-encrypt-unavailable')).toBe(true);
    expect(bodyText()).toContain('本机密钥不可用');
    const el = switchEl()!;
    expect(el.hasAttribute('disabled')).toBe(true);
    await act(async () => { el.click(); });
    await act(async () => { await Promise.resolve(); });
    expect(hasTestId('official-encrypt-confirm')).toBe(false);
    expect(spy.calls.length).toBe(0);
  });

  it('非管理员（can_admin=false）：不渲染高级区（后端同样 403）', async () => {
    await renderDialog({ authorized: false, can_admin: false, ui_known: true, ui_supported: true });
    expect(hasTestId('official-session-encryption')).toBe(false);
    expect(switchEl()).toBeNull();
    expect(hasButton('加密保存会话凭据')).toBe(false);
  });

  it('已加密 + 可关闭：关闭立即生效（不弹确认）', async () => {
    await renderDialog({ authorized: true, can_admin: true, session_encrypted: true, encryption_available: true });
    expect(switchEl()!.getAttribute('aria-checked')).toBe('true');
    await act(async () => { switchEl()!.click(); });
    await act(async () => { await Promise.resolve(); });
    expect(spy.calls).toEqual([false]);
    expect(hasTestId('official-encrypt-confirm')).toBe(false);
    expect(switchEl()!.getAttribute('aria-checked')).toBe('false');
  });

  // 2026-11-26 盲点修复（用户实报「确认开启后开关还是关的」）：
  // 未连接时开启只改意图（session_encryption_on=true、文件还没生成），
  // 开关必须保持「开」，并说明下次保存才落盘。
  it('未连接时开启：开关保持「开」（意图），提示下次保存生效', async () => {
    await renderDialog({ authorized: false, can_admin: true, encryption_available: true, session_encryption_on: true, session_encrypted: false });
    expect(switchEl()!.getAttribute('aria-checked')).toBe('true');
    expect(hasTestId('official-encrypt-pending')).toBe(true);
    expect(bodyText()).toContain('下次保存');
  });

  // 意图开但文件仍是明文（旧版本重授权覆盖）：开关为开，但要明确告知不一致。
  it('意图开而文件仍明文（不一致）：开关为开 + 显式提示', async () => {
    await renderDialog({ authorized: true, can_admin: true, encryption_available: true, session_encryption_on: true, session_encrypted: false, encryption_mismatch: true });
    expect(switchEl()!.getAttribute('aria-checked')).toBe('true');
    expect(hasTestId('official-encrypt-pending')).toBe(true);
    expect(bodyText()).toContain('仍是明文');
  });
});
