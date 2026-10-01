// sw.js 逻辑级测试：Cache API polyfill + 事件驱动，验证
// 红线拦截 / cache-first / 404 不缓存 / 版本轮换 / LRU / 旁路
import { readFileSync } from "node:fs";
import assert from "node:assert";

// ---------- Cache API polyfill（Map 语义近似） ----------
class CachePoly {
  constructor() { this.m = new Map(); }
  async match(req) {
    const k = typeof req === "string" ? req : req.url;
    const e = this.m.get(k);
    return e ? new Response(e.body, { status: 200, headers: e.headers }) : undefined;
  }
  async put(req, res) {
    const k = typeof req === "string" ? req : req.url;
    const body = await res.clone().text();
    this.m.set(k, { body, headers: res.headers });
  }
  async delete(req) {
    const k = typeof req === "string" ? req : req.url;
    return this.m.delete(k);
  }
  async keys() { return [...this.m.keys()].map((u) => new Request(u)); }
}
class CachesPoly {
  constructor() { this.m = new Map(); }
  async keys() { return [...this.m.keys()]; }
  async open(name) {
    if (!this.m.has(name)) this.m.set(name, new CachePoly());
    return this.m.get(name);
  }
  async delete(name) { return this.m.delete(name); }
  all() { return this.m; }
}
const caches = new CachesPoly();

// ---------- 上游模拟 ----------
const upstream = new Map(); // url -> body
upstream.set("https://nas/app/moo/api/apps/a@src/asset?type=icon", "ICON-A");
upstream.set("https://nas/app/moo/api/apps/b@src/asset?type=icon", "ICON-B");
// c@src 先 404（图标未预热），后补上
let netCalls = 0;
const realFetch = globalThis.fetch;
globalThis.fetch = async (url, opts) => {
  const u = typeof url === "string" ? url : url.url;
  if (u.endsWith("/api/icons/version")) {
    return Response.json({ v: STAMP });
  }
  if (ICON_URL_ALL.test(u) || u.includes("/app/moo/api/apps/")) {
    netCalls++;
    if (upstream.has(u)) return new Response(upstream.get(u), { status: 200, headers: { "content-type": "image/png" } });
    return new Response("nf", { status: 404 });
  }
  throw new Error("unexpected fetch: " + u);
};
let STAMP = "2026-09-23T21:30:04+08:00";
const ICON_URL_ALL = /\/app\/moo\/api\/apps\/[^/?#]+\/asset\?type=icon(\?|$)/;

// ---------- self 事件总线 ----------
let handlers = {};
let selfShim = null;
const swSrc = readFileSync(new URL("../frontend/public/sw.js", import.meta.url), "utf8");

// 在受限作用域执行 sw.js（它只用 self/caches/fetch/Response/Request）
function bootSW() {
  handlers = {};
  selfShim = {
    addEventListener(type, fh) { (handlers[type] ||= []).push(fh); },
    skipWaiting() {},
    clients: { claim: async () => {} },
  };
  const fn = new Function("self", "caches", "fetch", "Response", "Request", swSrc);
  fn(selfShim, caches, globalThis.fetch, Response, Request);
}
bootSW();

async function fire(type, event) {
  for (const h of handlers[type] || []) await h(event);
}
function iconEvent(url, cookie) {
  const req = new Request(url, { headers: cookie ? { cookie } : {} });
  return { request: req, respondWith(p) { this.resp = p; } };
}

// ========== 1. install/activate ==========
await fire("install", {});
await fire("activate", { waitUntil: async () => {} });
console.log("✓ install/activate");

// ========== 2. 首次图标：miss → 网络 → 入缓存 ==========
let ev = iconEvent("https://nas/app/moo/api/apps/a@src/asset?type=icon");
await fire("fetch", ev);
assert(ev.resp, "图标请求应被拦截");
let r = await ev.resp;
assert.strictEqual(await r.text(), "ICON-A");
assert(netCalls === 1, "首次应走网络");

// ========== 3. 再访：缓存命中，网络不再动 ==========
ev = iconEvent("https://nas/app/moo/api/apps/a@src/asset?type=icon");
await fire("fetch", ev);
r = await ev.resp;
assert.strictEqual(await r.text(), "ICON-A");
assert(netCalls === 1, "二次访问不应再打网络");
console.log("✓ cache-first（二次访问零网络）");

// ========== 4. 404 不缓存；上游补图后自然命中 ==========
ev = iconEvent("https://nas/app/moo/api/apps/c@src/asset?type=icon");
await fire("fetch", ev);
r = await ev.resp;
assert.strictEqual(r.status, 404, "未预热图标应 404");
ev = iconEvent("https://nas/app/moo/api/apps/c@src/asset?type=icon");
await fire("fetch", ev);
r = await ev.resp;
assert.strictEqual(r.status, 404, "404 不应被缓存");
upstream.set("https://nas/app/moo/api/apps/c@src/asset?type=icon", "ICON-C");
ev = iconEvent("https://nas/app/moo/api/apps/c@src/asset?type=icon");
await fire("fetch", ev);
r = await ev.resp;
assert.strictEqual(await r.text(), "ICON-C", "补图后应命中");
console.log("✓ 404 不缓存（补图自然命中）");

// ========== 5. 红线：非图标请求不拦截 ==========
const appList = { request: new Request("https://nas/app/moo/api/apps") };
handlers.fetch?.[0](appList);
assert(!appList.resp, "列表 API 不得被 SW 拦截");
const other = { request: new Request("https://nas/app-center-static/icon/Gitea/icon.png") };
handlers.fetch?.[0](other);
assert(!other.resp, "app-center-static 不得被 SW 拦截");
const sse = { request: new Request("https://nas/app/moo/api/apps/a@src/task/pause", { method: "POST" }) };
handlers.fetch?.[0](sse);
assert(!sse.resp, "POST 不得被拦截");
console.log("✓ 红线（仅拦社区图标 GET）");

// ========== 6. 版本轮换：版本戳变化 → 旧缓存清除、重新拉取 ==========
// 新 SW 生命周期（页面再次访问时 SW 重启，版本戳缓存失效）
bootSW();
const before = netCalls;
STAMP = "2026-09-23T23:00:00+08:00";
upstream.set("https://nas/app/moo/api/apps/a@src/asset?type=icon", "ICON-A-v2");
ev = iconEvent("https://nas/app/moo/api/apps/a@src/asset?type=icon");
await fire("fetch", ev);
r = await ev.resp;
assert.strictEqual(await r.text(), "ICON-A-v2", "版本变化后应拉到新图标");
assert(netCalls > before, "版本变化后应回源");
console.log("✓ 版本轮换（源同步后图标刷新）");

// ========== 7. 调试旁路 ==========
const b1 = { request: new Request("https://nas/app/moo/api/apps/b@src/asset?type=icon&nocache=1") };
handlers.fetch?.[0](b1);
assert(!b1.resp, "?nocache=1 应旁路");
const nBefore = netCalls;
const b2 = iconEvent("https://nas/app/moo/api/apps/b@src/asset?type=icon", "moo_no_sw_cache=1");
await fire("fetch", b2);
assert(!b2.resp, "cookie 旁路应生效");
console.log("✓ 调试旁路（URL 参数 + cookie）");

// ========== 8. LRU：605 个图标 → 淘汰最旧 5 个 ==========
for (let i = 0; i < 605; i++) {
  const u = `https://nas/app/moo/api/apps/lru${i}@src/asset?type=icon`;
  upstream.set(u, `BODY-${i}`);
  const e2 = iconEvent(u);
  await fire("fetch", e2);
  await e2.resp;
  await new Promise((r2) => setTimeout(r2, 1)); // 让 meta 时间戳错开
}
const meta = caches.all().get("moo-icons-meta");
assert(meta.m.size <= 600, `meta 应 ≤600，实际 ${meta.m.size}`);
// 最旧的 5 个（lru0..lru4）图标应被淘汰
const iconCacheName = [...caches.all().keys()].find((k) => k.startsWith("moo-icons-v"));
const ic = caches.all().get(iconCacheName);
for (let i = 0; i < 5; i++) {
  assert(!ic.m.has(`https://nas/app/moo/api/apps/lru${i}@src/asset?type=icon`), `lru${i} 应被淘汰`);
}
assert(ic.m.has("https://nas/app/moo/api/apps/lru604@src/asset?type=icon"), "最新条目应保留");
console.log("✓ LRU（600 上限淘汰最旧）");

console.log("\nALL PASS — sw.js 逻辑 8 组断言全过");
