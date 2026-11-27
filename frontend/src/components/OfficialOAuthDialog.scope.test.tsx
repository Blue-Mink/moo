// @vitest-environment jsdom
/**
 * 2026-11-26（加固 A）前端：官方源「授权范围自选」+ 失败处理。
 * 2026-11-27（批次 3c · D4）：面板拒绝 minimal 时**不再静默扩权**，改为用户显式确认。
 *
 * 锁住七条：
 *  1. 默认 full（卡片选中 full），发起授权带 scope=full；
 *  2. 切 minimal → 立即写设置（记住上次选择，不用 localStorage），
 *     随后发起授权带 scope=minimal；
 *  3. 回调返回 error_code=scope_rejected → **不再自动以 full 重发**（请求仍只有原本那次、
 *     config 未写 full），改为弹 AlertDialog 确认框（两个按钮 + 仅一条提示）；
 *  4. 点「改用完整权限重试」→ 写 config=full 并以 full 重发；
 *  5. 点「取消」→ 不发请求、不写 config、停留验证码视图 + 可操作提示，确认框关闭；
 *  6. 手动「改用完整权限重试」入口保留（覆盖「授权页根本没打开」拿不到回调信号）；
 *  7. 旧后端（status 没有 scope_minimal_supported）→ 不出现范围选项、
 *     **不发 scope 参数**，请求与改动前逐字节一致。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';
import { toast } from 'sonner';

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const box = vi.hoisted(() => ({
  status: {
    authorized: false,
    can_admin: true,
    ui_known: true,
    ui_supported: true,
    scope_minimal_supported: true,
    scope_mode: 'full',
  } as Record<string, unknown>,
  authorize: [] as Array<{ base?: string; scope?: string }>,
  settings: [] as Array<Record<string, unknown>>,
  callbackErrorCode: '' as string,
}));

vi.mock('sonner', () => ({
  toast: { info: vi.fn(), error: vi.fn(), success: vi.fn(), warning: vi.fn() },
}));

vi.mock('../api/client', async () => {
  const actual = await vi.importActual<typeof import('../api/client')>('../api/client');
  return {
    ...actual,
    fetchOfficialStatus: async () => box.status,
    officialAuthorize: async (base?: string, scope?: string) => {
      box.authorize.push({ base, scope });
      return { url: 'http://panel.invalid/signin?code_challenge=x' };
    },
    officialCallback: async () => {
      if (box.callbackErrorCode) {
        // 用 actual 的类构造，保证对话框里 officialErrorCodeOf 的 instanceof 成立。
        throw new actual.OfficialCallbackError('面板拒绝了授权范围', 400, box.callbackErrorCode, 'minimal');
      }
    },
    officialCancel: async () => undefined,
    officialLogout: async () => undefined,
    officialAuthorizeHeadless: async () => undefined,
    fetchOfficialApps: async () => ({ total: 3, list: [] }),
    setOfficialSessionEncryption: async () => ({ ok: true, encrypted: false }),
    updateSettings: async (patch: Record<string, unknown>) => {
      box.settings.push(patch);
      return { ok: true };
    },
  };
});

const { OfficialOAuthDialog } = await import('./OfficialOAuthDialog');

let container: HTMLDivElement | null = null;
let root: Root | null = null;

beforeEach(() => {
  box.authorize = [];
  box.settings = [];
  box.callbackErrorCode = '';
  vi.mocked(toast.info).mockClear();
  vi.mocked(toast.error).mockClear();
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
  box.status = status;
  await act(async () => {
    root!.render(<OfficialOAuthDialog open onOpenChange={() => {}} />);
  });
  await act(async () => { await Promise.resolve(); });
}

const byTestId = (id: string) => document.querySelector(`[data-testid="${id}"]`) as HTMLElement | null;
const buttonByText = (label: string) =>
  Array.from(document.querySelectorAll('button')).find((b) => (b.textContent || '').includes(label)) as HTMLElement | undefined;

function setInputValue(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
  setter.call(el, value);
  el.dispatchEvent(new Event('input', { bubbles: true }));
}

const baseStatus = { authorized: false, can_admin: true, ui_known: true, ui_supported: true };

/** 以 minimal 发起授权 → 进「输入验证码」视图 → 提交一个验证码（触发回调）。
 *  jsdom 里 window.open 返回 null → 走 popupBlocked 分支，同样进入 code 视图。 */
async function startMinimalAndSubmitCode() {
  await act(async () => { buttonByText('新标签页授权')!.click(); });
  await act(async () => { await Promise.resolve(); });
  const input = document.querySelector('input[placeholder^="例如"]') as HTMLInputElement;
  await act(async () => { setInputValue(input, 'CODE123'); });
  await act(async () => { buttonByText('提交验证码')!.click(); });
  await act(async () => { await Promise.resolve(); });
}

describe('官方源 · 授权范围自选（加固 A）', () => {
  it('默认 full：两张卡片都渲染、full 选中，发起授权带 scope=full', async () => {
    await renderDialog({ ...baseStatus, scope_minimal_supported: true, scope_mode: 'full' });
    expect(byTestId('official-scope-choices')).not.toBeNull();
    expect(byTestId('official-scope-full')!.getAttribute('aria-checked')).toBe('true');
    expect(byTestId('official-scope-minimal')!.getAttribute('aria-checked')).toBe('false');
    expect(document.body.textContent).toContain('不能读写文件');

    await act(async () => { buttonByText('OAuth授权登录')!.click(); });
    await act(async () => { await Promise.resolve(); });
    expect(box.authorize).toHaveLength(1);
    expect(box.authorize[0].scope).toBe('full');
    expect(box.settings).toHaveLength(0); // 只是默认值，不该有写入
  });

  it('上次选过 minimal（status.scope_mode）→ 打开即选中 minimal', async () => {
    await renderDialog({ ...baseStatus, scope_minimal_supported: true, scope_mode: 'minimal' });
    expect(byTestId('official-scope-minimal')!.getAttribute('aria-checked')).toBe('true');
    expect(byTestId('official-scope-full')!.getAttribute('aria-checked')).toBe('false');
  });

  it('切 minimal → 立即写设置记住选择，随后发起授权带 scope=minimal', async () => {
    await renderDialog({ ...baseStatus, scope_minimal_supported: true, scope_mode: 'full' });
    await act(async () => { byTestId('official-scope-minimal')!.click(); });
    await act(async () => { await Promise.resolve(); });
    expect(box.settings).toEqual([{ official_scope_mode: 'minimal' }]);
    expect(byTestId('official-scope-minimal')!.getAttribute('aria-checked')).toBe('true');

    await act(async () => { buttonByText('OAuth授权登录')!.click(); });
    await act(async () => { await Promise.resolve(); });
    expect(box.authorize[0].scope).toBe('minimal');
  });

  // ── 批次 3c · D4：面板拒绝 minimal → 用户显式确认（不再静默扩权） ──

  it('回调 scope_rejected → 不再自动重发，改为弹确认框（config 未写 full）', async () => {
    box.callbackErrorCode = 'scope_rejected';
    await renderDialog({ ...baseStatus, scope_minimal_supported: true, scope_mode: 'minimal' });
    await startMinimalAndSubmitCode();

    // 只有原本那一次（minimal）请求，**没有**自动以 full 重发
    expect(box.authorize.map((a) => a.scope)).toEqual(['minimal']);
    // full 只能由用户点确认后写入 → 此刻 config 一次都没写
    expect(box.settings).toHaveLength(0);
    // 仍停在验证码视图（没跳去内嵌授权页）
    expect(document.querySelector('iframe')).toBeNull();

    const dlg = byTestId('official-scope-rejected');
    expect(dlg).not.toBeNull();
    expect(dlg!.getAttribute('role')).toBe('alertdialog');
    expect(dlg!.textContent).toContain('面板不支持「仅应用中心」授权范围');
    expect(dlg!.textContent).toContain('权限更大');
    expect(byTestId('official-scope-rejected-confirm')!.textContent).toContain('改用完整权限重试');
    expect(byTestId('official-scope-rejected-cancel')!.textContent).toContain('取消');
    // 一条提示足够，不与弹窗叠加第二条 toast
    expect(vi.mocked(toast.info)).toHaveBeenCalledTimes(1);
    expect(
      vi.mocked(toast.info).mock.calls.some((c) => String(c[0]).includes('自动')),
    ).toBe(false);
  });

  it('确认「改用完整权限重试」→ 写 config=full 并以 full 重发', async () => {
    box.callbackErrorCode = 'scope_rejected';
    await renderDialog({ ...baseStatus, scope_minimal_supported: true, scope_mode: 'minimal' });
    await startMinimalAndSubmitCode();

    await act(async () => { byTestId('official-scope-rejected-confirm')!.click(); });
    await act(async () => { await Promise.resolve(); });
    await act(async () => { await Promise.resolve(); });

    expect(box.settings).toEqual([{ official_scope_mode: 'full' }]);
    expect(box.authorize.map((a) => a.scope)).toEqual(['minimal', 'full']);
    // 确认后关掉确认框、按 full 走内嵌授权视图
    expect(byTestId('official-scope-rejected')).toBeNull();
    expect(document.querySelector('iframe')).not.toBeNull();
  });

  it('确认框点「取消」→ 不发请求、不写 config、提示可见、对话框关闭', async () => {
    box.callbackErrorCode = 'scope_rejected';
    await renderDialog({ ...baseStatus, scope_minimal_supported: true, scope_mode: 'minimal' });
    await startMinimalAndSubmitCode();
    expect(byTestId('official-scope-rejected')).not.toBeNull();

    await act(async () => { byTestId('official-scope-rejected-cancel')!.click(); });
    await act(async () => { await Promise.resolve(); });

    // 不扩权：没有新请求、没有写 config
    expect(box.authorize.map((a) => a.scope)).toEqual(['minimal']);
    expect(box.settings).toHaveLength(0);
    // 确认框关闭、停留在验证码视图
    expect(byTestId('official-scope-rejected')).toBeNull();
    expect(document.querySelector('input[placeholder^="例如"]')).not.toBeNull();
    // 留一条可操作提示（说明未改动 + 指向手动路径）
    const hint = byTestId('official-scope-cancel-hint');
    expect(hint).not.toBeNull();
    expect(hint!.textContent).toContain('授权范围仍是「仅应用中心」');
    expect(hint!.textContent).toContain('没有改动任何设置');
    expect(hint!.textContent).toContain('改用完整权限重试');
    // 手动兜底入口仍在（scopeMode 仍是 minimal）
    expect(byTestId('official-scope-retry-full')).not.toBeNull();
  });

  it('手动「改用完整权限重试」：覆盖授权页根本没打开的情况', async () => {
    await renderDialog({ ...baseStatus, scope_minimal_supported: true, scope_mode: 'minimal' });
    await act(async () => { buttonByText('新标签页授权')!.click(); });
    await act(async () => { await Promise.resolve(); });

    const retry = byTestId('official-scope-retry-full');
    expect(retry).not.toBeNull();
    await act(async () => { retry!.click(); });
    await act(async () => { await Promise.resolve(); });

    expect(box.authorize.map((a) => a.scope)).toEqual(['minimal', 'full']);
    expect(box.settings).toEqual([{ official_scope_mode: 'full' }]);
  });

  it('旧后端（无 scope_minimal_supported）：不出现范围选项、不发 scope 参数', async () => {
    await renderDialog({ ...baseStatus });
    expect(byTestId('official-scope-choices')).toBeNull();

    await act(async () => { buttonByText('OAuth授权登录')!.click(); });
    await act(async () => { await Promise.resolve(); });
    expect(box.authorize).toHaveLength(1);
    expect(box.authorize[0].scope).toBeUndefined(); // 请求与改动前完全一致
    expect(box.settings).toHaveLength(0);
  });
});
