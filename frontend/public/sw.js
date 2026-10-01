/* Moo 图标 Service Worker（客户端持久缓存层）
 *
 * 目标：看过的社区图标进 Cache Storage（本机磁盘）——列表再访问 ~0ms，
 * 网关抖动/断网时已浏览图标照常渲染（不塌成占位框）。
 *
 * 红线：只拦截 Moo 社区图标资产（ICON_URL 正则，含路径前缀），
 * 绝不碰 app-center-static、其他应用、SSE、API JSON。
 *
 * 失效：图标集版本戳 = 最近源同步时间（GET /api/icons/version）。
 * 版本变化 ⇒ 新 versioned 缓存名 + 删旧缓存（标准 versioned cache 模式）。
 * 404 不缓存：图标后来被预热/源补上时，下次自然命中。
 *
 * 调试旁路：图标 URL 加 ?nocache=1，或 document.cookie 设 moo_no_sw_cache=1
 * （调试完在 DevTools → Application → Cookies 清掉）。
 *
 * 注意：仅在安全上下文（https 或 localhost）注册——见 main.tsx。
 */
"use strict";

// 0.6.200：type=icon 后允许跟随 & 参数（&v=<版本> 版本戳）——
// 旧版 (?|$) 不匹配 &v=，带戳 URL 会漏出 SW 缓存（离线图标回归）。
const ICON_URL = /\/app\/moo\/api\/apps\/[^/?#]+\/asset\?type=icon([&?]|$)/;
const VERSION_URL = "/app/moo/api/icons/version";
const CACHE_PREFIX = "moo-icons-v";
const META_CACHE = "moo-icons-meta";
const MAX_ENTRIES = 600; // 600 × ~50KB ≈ 30MB 上限，LRU 淘汰

let versionPromise = null;
let versionAt = 0;
const VERSION_TTL = 60000; // 版本戳 60s 缓存：长开页面里源同步后 1 分钟内感知
let cleanedFor = null;

function getStamp() {
  if (!versionPromise || Date.now() - versionAt > VERSION_TTL) {
    versionAt = Date.now();
    versionPromise = fetch(VERSION_URL, { cache: "no-store" })
      .then((r) => (r.ok ? r.json() : { v: "" }))
      .then((j) => (j && j.v) || "init")
      .catch(() => "init");
  }
  return versionPromise;
}

function iconCacheName(v) {
  return CACHE_PREFIX + String(v).replace(/[^A-Za-z0-9_-]/g, "");
}

async function purgeOldCaches(keepName) {
  const keys = await caches.keys();
  await Promise.all(
    keys
      .filter((k) => k.startsWith(CACHE_PREFIX) && k !== keepName)
      .map((k) => caches.delete(k)),
  );
}

self.addEventListener("install", () => {
  self.skipWaiting();
});

self.addEventListener("activate", (event) => {
  event.waitUntil((async () => {
    const name = iconCacheName(await getStamp());
    await purgeOldCaches(name);
    await self.clients.claim();
  })());
});

self.addEventListener("fetch", (event) => {
  const req = event.request;
  if (req.method !== "GET") return;
  let url;
  try {
    url = new URL(req.url);
  } catch {
    return;
  }
  // 红线：只处理社区图标资产（完整路径含 /app/moo/ 前缀）
  if (!ICON_URL.test(url.pathname + url.search)) return;
  // 调试旁路
  if (url.searchParams.has("nocache")) return;
  if ((req.headers.get("cookie") || "").includes("moo_no_sw_cache=1")) return;
  event.respondWith(serveIcon(req));
});

async function serveIcon(req) {
  const v = await getStamp();
  const name = iconCacheName(v);
  // 每个 SW 生命周期的首次图标请求：清掉旧版本缓存
  // （activate 只在脚本变化时跑，版本戳变化时靠这里兜底）
  if (cleanedFor !== name) {
    cleanedFor = name;
    await purgeOldCaches(name);
  }
  const cache = await caches.open(name);
  const hit = await cache.match(req);
  if (hit) return hit;
  let res;
  try {
    res = await fetch(req);
  } catch (e) {
    // 网关不可达：宁可 503 也不留洞（img 会走 onError 占位图）
    return new Response("gateway unreachable", { status: 503 });
  }
  if (res.ok) {
    await cache.put(req, res.clone());
    await touchMeta(req.url, name);
  }
  // 非 ok（404/5xx）不缓存
  return res;
}

// LRU：meta 里每个图标存访问时间戳，超 MAX_ENTRIES 淘汰最旧
async function touchMeta(key, iconName) {
  try {
    const meta = await caches.open(META_CACHE);
    await meta.put(new Request(key), new Response(JSON.stringify({ t: Date.now() })));
    const keys = await meta.keys();
    if (keys.length <= MAX_ENTRIES) return;
    const entries = [];
    for (const k of keys) {
      let t = 0;
      try {
        t = (await (await meta.match(k)).json()).t;
      } catch {}
      entries.push([k, t]);
    }
    entries.sort((a, b) => a[1] - b[1]);
    const iconCache = await caches.open(iconName);
    for (const [k] of entries.slice(0, keys.length - MAX_ENTRIES)) {
      await meta.delete(k);
      await iconCache.delete(k);
    }
  } catch {
    // meta 失败不影响图标返回
  }
}
