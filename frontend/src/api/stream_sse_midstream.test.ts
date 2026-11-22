// @vitest-environment jsdom
/**
 * SSE_RESUME（B 方案）：流式请求的断线续传。
 *
 * 复现（用户侧 2026-11-21 报障）：iOS WebKit 上点「更新」，SSE 连接在**响应头已到、
 * 事件流已经开始**之后被中断（切网/后台挂起/网关空闲超时），WebKit 在读流阶段抛
 * `TypeError: Load failed`；而服务端操作在后台继续跑完 → 旧实现把该异常原样上抛，
 * 用户看到「更新请求失败 + Load failed」，实际却更新成功。
 *
 * 现在的行为：事件带 opID → 断流后先推 reconnecting，再 GET
 * /api/operations/{id}/events 续传（无 id 时按 kind+target 兜底匹配）；
 * 彻底失败才抛错，且带 background=true + opId（UI 改为「操作仍在后台继续」+ 刷新）。
 */
import { beforeAll, describe, expect, it, vi } from 'vitest';

const enc = new TextEncoder();

type Route = (url: string, init?: RequestInit) => Response | Promise<Response>;
let route: Route = () => { throw new Error('unexpected fetch'); };
const calls: string[] = [];

const stub = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
  const url = String(input);
  calls.push(url);
  return route(url, init);
});

/** 读到一半就断的流：先吐一条事件，再抛读流异常（WebKit 的 Load failed）。 */
function brokenAfter(firstEvent: string): Response {
  let pulled = 0;
  const body = new ReadableStream<Uint8Array>({
    pull(c) {
      pulled += 1;
      if (pulled === 1) {
        c.enqueue(enc.encode(firstEvent));
        return;
      }
      c.error(new TypeError('Load failed'));
    },
  });
  return new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream' } });
}

/** 一次性把给定事件吐完并正常关闭的流。 */
function sseOf(events: string[]): Response {
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      for (const e of events) c.enqueue(enc.encode(e));
      c.close();
    },
  });
  return new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream' } });
}

function jsonOf(v: unknown, status = 200): Response {
  return new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
}

let updateApp: typeof import('./client')['updateApp'];

beforeAll(async () => {
  // client.ts 在模块加载时 window.fetch.bind(window)，必须先替换再 import
  (window as unknown as { fetch: typeof fetch }).fetch = stub as unknown as typeof fetch;
  vi.stubGlobal('fetch', stub);
  const client = await import('./client');
  updateApp = client.updateApp;
  // P0-1：把退避压到毫秒级，避免用例真等 1+2+4s + 3s 轮询。
  client.__sseTimingForTests.resumeBackoff = [10, 10, 10];
  client.__sseTimingForTests.pollBackoff = [10];
  client.__sseTimingForTests.pollTimeoutMs = 2000;
  client.__sseTimingForTests.pollMaxMisses = 2;
});

const UPDATE_URL = '/api/apps/fnmonitor/update';


describe('streamSSE 中途断流', () => {
  // a) 事件带 id：续传走 /api/operations/{id}/events，拿到 done 即正常 resolve。
  it('a) 读流中断 + 事件带 id → 续传拿到 done', async () => {
    calls.length = 0;
    route = (url) => {
      if (url === UPDATE_URL) {
        return brokenAfter('data: {"step":"update","progress":12,"id":"op-1","message":"下载安装包…"}\n\n');
      }
      if (url === '/api/operations/op-1/events') {
        return sseOf([
          'data: {"step":"update","progress":80,"id":"op-1","message":"安装中…"}\n\n',
          'data: {"step":"done","progress":100,"id":"op-1","message":"更新完成"}\n\n',
        ]);
      }
      throw new Error(`unexpected url: ${url}`);
    };
    const seen: string[] = [];
    await updateApp('fnmonitor', (ev) => { seen.push(String(ev.step)); }).promise;
    expect(seen).toContain('reconnecting');          // 先告诉用户「正在恢复」
    expect(seen[seen.length - 1]).toBe('done');      // 终态按 done 结算
    expect(calls).toContain('/api/operations/op-1/events');
  });

  // b) 事件无 id（首条就断）：按 kind+target + 5 分钟窗口兜底匹配后跟随。
  it('b) 事件不带 id → /api/operations 兜底匹配后跟随', async () => {
    calls.length = 0;
    route = (url) => {
      if (url === UPDATE_URL) {
        return brokenAfter('data: {"step":"update","progress":5,"message":"下载安装包…"}\n\n');
      }
      if (url === '/api/operations') {
        return jsonOf({
          current: { id: 'op-2', kind: 'update', target: 'fnmonitor', state: 'running', started_at: new Date().toISOString() },
          history: [
            // 窗口外 / 别的应用：都不能被误匹配
            { id: 'old', kind: 'update', target: 'fnmonitor', state: 'running', started_at: new Date(Date.now() - 60 * 60 * 1000).toISOString() },
            { id: 'other', kind: 'update', target: 'gitea', state: 'running', started_at: new Date().toISOString() },
          ],
        });
      }
      if (url === '/api/operations/op-2/events') {
        return sseOf(['data: {"step":"done","progress":100,"id":"op-2","message":"更新完成"}\n\n']);
      }
      throw new Error(`unexpected url: ${url}`);
    };
    const seen: string[] = [];
    await updateApp('fnmonitor', (ev) => { seen.push(String(ev.step)); }).promise;
    expect(seen).toContain('reconnecting');
    expect(seen[seen.length - 1]).toBe('done');
    expect(calls).toContain('/api/operations/op-2/events');
  });

  // c) 续传 404 且兜底匹配不到：抛中文错误 + background=true（UI 据此提示后台继续）。
  it('c) 续传与兜底都失败 → 中文错误 + background=true + opId', async () => {
    route = (url) => {
      if (url === UPDATE_URL) {
        return brokenAfter('data: {"step":"update","progress":5,"id":"op-3","message":"安装中…"}\n\n');
      }
      if (url === '/api/operations/op-3/events') return jsonOf({ error: '操作不存在或已过期' }, 404);
      if (url === '/api/operations/op-3') return jsonOf({ error: '操作不存在或已过期' }, 404);
      if (url === '/api/operations') return jsonOf({ current: null, history: [] });
      throw new Error(`unexpected url: ${url}`);
    };
    let caught: unknown = null;
    try {
      await updateApp('fnmonitor', () => {}).promise;
    } catch (e) {
      caught = e;
    }
    expect(caught).toBeInstanceOf(Error);
    const err = caught as Error & { background?: boolean; opId?: string };
    expect(err.message).toContain('后台继续');
    expect(err.message).toContain('请稍后查看已安装列表');
    expect(err.background).toBe(true);
    expect(err.opId).toBe('op-3');
  }, 15000);

  // d) 连接阶段失败（响应头都没到）仍包成中文——与原行为一致，不得回归。
  it('d) 连接阶段失败被包成中文', async () => {
    route = () => { throw new TypeError('Load failed'); };
    let caught: unknown = null;
    try {
      await updateApp('fnmonitor', () => {}).promise;
    } catch (e) {
      caught = e;
    }
    expect((caught as Error).message.startsWith('无法连接 Moo 服务：')).toBe(true);
  });

  // e) 旧服务端干净 EOF（事件无 id、流自己结束）：保持既有行为，resolve 不误报失败。
  it('e) 旧服务端无 id 且流干净结束 → 不抛错', async () => {
    route = (url) => {
      if (url === UPDATE_URL) return sseOf(['data: {"step":"done","progress":100,"message":"更新完成"}\n\n']);
      throw new Error(`unexpected url: ${url}`);
    };
    await expect(updateApp('fnmonitor', () => {}).promise).resolves.toBeUndefined();
  });

  // f) P0-1：续传也失败（404）→ 按 opID 轮询，running → done 仍能正常结算。
  it('f) 续传失败后轮询到 done → 正常 resolve', async () => {
    calls.length = 0;
    let polls = 0;
    route = (url) => {
      if (url === UPDATE_URL) {
        return brokenAfter('data: {"step":"update","progress":12,"id":"op-6","message":"下载安装包…"}\n\n');
      }
      if (url === '/api/operations/op-6/events') return jsonOf({ error: '操作不存在或已过期' }, 404);
      if (url === '/api/operations') return jsonOf({ current: null, history: [] });
      if (url === '/api/operations/op-6') {
        polls += 1;
        return polls === 1
          ? jsonOf({ id: 'op-6', kind: 'update', target: 'fnmonitor', state: 'running', progress: 60, msg: '安装中…' })
          : jsonOf({ id: 'op-6', kind: 'update', target: 'fnmonitor', state: 'done', progress: 100, msg: '更新完成' });
      }
      throw new Error(`unexpected url: ${url}`);
    };
    const seen: string[] = [];
    await updateApp('fnmonitor', (ev) => { seen.push(String(ev.step)); }).promise;
    expect(polls).toBeGreaterThanOrEqual(2);
    expect(seen[seen.length - 1]).toBe('done');
    expect(calls).toContain('/api/operations/op-6');
  }, 15000);

  // h) P1-2：事件带 id 但流「干净结束」（网关正常关流、没收到 done/error）
  //    → 仍必须续传/轮询取回真实终态。旧实现只看 readErr，此处会直接
  //    backgroundError，后台成功/失败永远送不回 UI。
  it('h) 事件带 id + 干净 EOF（无终态）→ 续传拿到 done', async () => {
    calls.length = 0;
    route = (url) => {
      if (url === UPDATE_URL) {
        return sseOf(['data: {"step":"update","progress":40,"id":"op-8","message":"安装中…"}\n\n']);
      }
      if (url === '/api/operations/op-8/events') {
        return sseOf([
          'data: {"step":"update","progress":90,"id":"op-8","message":"收尾…"}\n\n',
          'data: {"step":"done","progress":100,"id":"op-8","message":"更新完成"}\n\n',
        ]);
      }
      throw new Error(`unexpected url: ${url}`);
    };
    const seen: string[] = [];
    await updateApp('fnmonitor', (ev) => { seen.push(String(ev.step)); }).promise;
    expect(seen).toContain('reconnecting');
    expect(seen[seen.length - 1]).toBe('done');
    expect(calls).toContain('/api/operations/op-8/events');
  });

  // g) P0-1：轮询拿到 error → 报真实原因，而不是「操作仍在后台继续」。
  it('g) 轮询到 error → 抛真实失败原因', async () => {
    route = (url) => {
      if (url === UPDATE_URL) {
        return brokenAfter('data: {"step":"update","progress":10,"id":"op-7","message":"下载安装包…"}\n\n');
      }
      if (url === '/api/operations/op-7/events') return jsonOf({ error: '操作不存在或已过期' }, 404);
      if (url === '/api/operations') return jsonOf({ current: null, history: [] });
      if (url === '/api/operations/op-7') {
        return jsonOf({ id: 'op-7', kind: 'update', target: 'fnmonitor', state: 'error', error: '安装失败：磁盘空间不足' });
      }
      throw new Error(`unexpected url: ${url}`);
    };
    let caught: unknown = null;
    try {
      await updateApp('fnmonitor', () => {}).promise;
    } catch (e) {
      caught = e;
    }
    expect(caught).toBeInstanceOf(Error);
    const err = caught as Error & { background?: boolean };
    expect(err.message).toContain('磁盘空间不足');
    expect(err.background).toBeUndefined();
  }, 15000);
});
