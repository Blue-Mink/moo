// @vitest-environment jsdom
/**
 * 0.6.5xx：官方 cloud 升级向导契约单测。
 *
 * 为什么必须有：后端升级确认失败时会回 `detail.kind='wizard_required'`，前端
 * 弹窗收集参数后**带 `?wizard=` 重入同一 POST /update**（后端 panelInstallSSE
 * 早已解析这个 query）。若 updateApp 忘了拼参数，用户会反复看到同一个确认弹窗、
 * 永远升不上去——而界面上「点了有反应」，不易察觉。
 */
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';

const sseDone = 'data: {"step":"done","progress":100,"message":"完成"}\n\n';

let urls: string[] = [];
const stub = vi.fn(async (input: RequestInfo | URL) => {
  urls.push(typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url);
  return new Response(sseDone, { status: 200, headers: { 'Content-Type': 'text/event-stream' } });
});

// client.ts 在模块加载时就 `window.fetch.bind(window)` 取走了 fetch，
// 所以必须先替换 window.fetch，再动态 import 模块。
let updateApp: typeof import('./client')['updateApp'];

beforeAll(async () => {
  (window as unknown as { fetch: typeof fetch }).fetch = stub as unknown as typeof fetch;
  vi.stubGlobal('fetch', stub);
  updateApp = (await import('./client')).updateApp;
});

beforeEach(() => {
  urls = [];
  stub.mockClear();
});

afterEach(() => {
  vi.clearAllMocks();
});

describe('updateApp 升级向导参数', () => {
  it('带 wizard 参数时拼 ?wizard=[{key,value}]', async () => {
    const handle = updateApp('motrix', () => {}, [{ key: 'download_dir', value: '/vol1/1000/Downloads' }]);
    await handle.promise;

    expect(urls).toHaveLength(1);
    const u = new URL(urls[0], 'http://localhost');
    expect(u.pathname).toBe('/api/apps/motrix/update');
    const raw = u.searchParams.get('wizard');
    expect(raw).toBeTruthy();
    expect(JSON.parse(raw as string)).toEqual([{ key: 'download_dir', value: '/vol1/1000/Downloads' }]);
  });

  it('无 wizard 参数时保持原 URL（不带空 query，避免缓存键漂移）', async () => {
    const handle = updateApp('motrix', () => {});
    await handle.promise;
    expect(urls[0]).toBe('/api/apps/motrix/update');
  });

  it('空数组视同未传', async () => {
    const handle = updateApp('motrix', () => {}, []);
    await handle.promise;
    expect(urls[0]).toBe('/api/apps/motrix/update');
  });
});
