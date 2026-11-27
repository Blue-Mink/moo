// @vitest-environment jsdom
/**
 * 盲点补测 批次 4a · T5（FIXPLAN 盲点矩阵 #5 核对：取消 / 超时 / 旧 code）
 *
 * 核对结论：OfficialOAuthDialog.scope.test.tsx（8 条）已覆盖范围自选与
 * scope_rejected 的确认/取消分支，但**没有**覆盖：
 *   ① 内嵌授权页 8s 未开 → 慢速提示条（超时）；
 *   ② 输入验证码视图点「取消授权」→ 调 officialCancel 并回未连接视图；
 *   ③ 提交一个已过期/被用过的旧验证码 → 透传后端文案、留在验证码视图、
 *      busy 复位、且不得误触 scope 确认框。
 * 本文件只补这三条，不重复既有用例。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';
import { toast } from 'sonner';

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const box = vi.hoisted(() => ({
  status: { authorized: false, can_admin: true, ui_known: true, ui_supported: true } as Record<string, unknown>,
  authorize: [] as Array<{ base?: string; scope?: string }>,
  cancel: 0,
  callbackError: null as null | { message: string; status: number; code: string; scope?: string },
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
      if (box.callbackError) {
        const e = box.callbackError;
        throw new actual.OfficialCallbackError(e.message, e.status, e.code, e.scope);
      }
    },
    officialCancel: async () => { box.cancel += 1; },
    officialLogout: async () => undefined,
    officialAuthorizeHeadless: async () => undefined,
    fetchOfficialApps: async () => ({ total: 3, list: [] }),
    setOfficialSessionEncryption: async () => ({ ok: true, encrypted: false }),
    updateSettings: async () => ({ ok: true }),
  };
});

const { OfficialOAuthDialog } = await import('./OfficialOAuthDialog');

let container: HTMLDivElement | null = null;
let root: Root | null = null;

beforeEach(() => {
  box.authorize = [];
  box.cancel = 0;
  box.callbackError = null;
  vi.mocked(toast.info).mockClear();
  vi.mocked(toast.error).mockClear();
  vi.mocked(toast.warning).mockClear();
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
  vi.useRealTimers();
});

async function renderDialog(status: Record<string, unknown> = { authorized: false, can_admin: true, ui_known: true, ui_supported: true }) {
  box.status = status;
  await act(async () => {
    root!.render(<OfficialOAuthDialog open onOpenChange={() => {}} />);
  });
  await act(async () => { await Promise.resolve(); });
}

const buttonByText = (label: string) =>
  Array.from(document.querySelectorAll('button')).find((b) => (b.textContent || '').includes(label)) as HTMLElement | undefined;

const codeInput = () => document.querySelector('input[placeholder^="例如"]') as HTMLInputElement | null;

function setInputValue(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
  setter.call(el, value);
  el.dispatchEvent(new Event('input', { bubbles: true }));
}

/** 进「输入验证码」视图（window.open 在 jsdom 返回 null → popupBlocked 分支同样落 code 视图）。 */
async function enterCodeView() {
  await act(async () => { buttonByText('新标签页授权')!.click(); });
  await act(async () => { await Promise.resolve(); });
  expect(codeInput()).not.toBeNull();
}

describe('官方源授权 · 取消 / 超时 / 旧 code（批次 4a · T5）', () => {
  // ① 超时：内嵌授权页 8s 未加载 → 出慢速提示（不自动关闭，给用户出口）。
  // 反向验证：把 setTimeout 的 8000 改成 60000 → 本用例 FAIL。
  it('内嵌授权页 8 秒未加载 → 出慢速提示条', async () => {
    vi.useFakeTimers();
    await renderDialog();

    await act(async () => { buttonByText('OAuth授权登录')!.click(); });
    await act(async () => { await Promise.resolve(); });
    expect(document.querySelector('iframe')).not.toBeNull();
    // 7.9s：还不该出提示（证明断言抓的是 8s 这条边界，而不是"一进来就有"）
    await act(async () => { vi.advanceTimersByTime(7900); });
    expect(document.body.textContent).not.toContain('授权页加载较慢或无法打开');

    await act(async () => { vi.advanceTimersByTime(200); });
    expect(document.body.textContent).toContain('授权页加载较慢或无法打开');
    // 提示里必须有可点的出口，而不是只让用户找右上角小箭头
    expect(buttonByText('在新标签页打开授权页')).toBeDefined();
    // 不自动关窗（视图仍在 iframe）
    expect(document.querySelector('iframe')).not.toBeNull();
  });

  // ② 取消授权：调 officialCancel 一次 + 回未连接视图（不残留验证码输入框）。
  // 反向验证：把 cancelAuth 里的 officialCancel() 调用删掉 → 本用例 FAIL。
  it('验证码视图点「取消授权」→ 调 officialCancel 并回到未连接视图', async () => {
    await renderDialog();
    await enterCodeView();
    expect(box.cancel).toBe(0);

    await act(async () => { buttonByText('取消授权')!.click(); });
    await act(async () => { await Promise.resolve(); });

    expect(box.cancel).toBe(1);
    expect(codeInput()).toBeNull();
    // 回到未连接视图：主按钮是「OAuth授权登录」
    expect(buttonByText('OAuth授权登录')).toBeDefined();
    expect(vi.mocked(toast.error)).not.toHaveBeenCalled();
  });

  // ③ 旧 code：后端回 invalid_grant（验证码已过期/已被用过）→ 透传文案、
  //    留在验证码视图可重试、busy 复位、且不得误弹 scope 确认框。
  // 反向验证：把 catch 里的 toast.error 换成固定文案 / 删掉 → 本用例 FAIL。
  it('提交旧验证码（invalid_grant）→ 透传后端文案并留在验证码视图', async () => {
    box.callbackError = { message: '验证码已过期或已被使用，请重新授权', status: 400, code: 'invalid_grant', scope: 'full' };
    await renderDialog();
    await enterCodeView();

    await act(async () => { setInputValue(codeInput()!, 'OLDCODE1'); });
    await act(async () => { buttonByText('提交验证码')!.click(); });
    await act(async () => { await Promise.resolve(); });
    await act(async () => { await Promise.resolve(); });

    // 透传后端提示（不是笼统的"发起授权失败"）
    expect(vi.mocked(toast.error)).toHaveBeenCalledTimes(1);
    expect(String(vi.mocked(toast.error).mock.calls[0][0])).toContain('验证码已过期或已被使用');
    // 留在验证码视图，可重新粘贴
    expect(codeInput()).not.toBeNull();
    expect(document.querySelector('iframe')).toBeNull();
    // 不得误触 scope 确认框（invalid_grant 不是 scope_rejected）
    expect(document.querySelector('[data-testid="official-scope-rejected"]')).toBeNull();
    // busy 复位：提交按钮重新可用（文案回到「提交验证码」）
    expect(buttonByText('提交验证码')).toBeDefined();
    expect(box.authorize).toHaveLength(1); // 只有进入 code 视图那次，未重发
  });
});
