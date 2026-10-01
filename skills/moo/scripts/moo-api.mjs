#!/usr/bin/env node
/**
 * moo-api.mjs — thin client for the Moo HTTP API (sanitized; no embedded secrets).
 *
 * Usage:
 *   node moo-api.mjs <method> <path> [json-body] [--base <api-base>] [--raw]
 *   node moo-api.mjs sse <method> <path> [json-body] [--base <api-base>]
 *
 * Examples:
 *   node moo-api.mjs GET /api/version --base http://<NAS_IP>:5666/app/moo/api
 *   node moo-api.mjs GET /api/sources
 *   node moo-api.mjs POST /api/sources/sync-all
 *   node moo-api.mjs PUT /api/settings '{"proxy_enabled":true,"proxy_url":"socks5://127.0.0.1:1080"}'
 *   node moo-api.mjs sse POST /api/apps/<key>/install
 *
 * Notes:
 * - Base URL default: MOO_API_BASE env or http://127.0.0.1:38101/api
 * - Write endpoints need the trusted channel (panel gateway base) + admin session.
 *   When calling through a browser-logged-in panel, pass the session cookie:
 *     --cookie "moosession=..." (or set MOO_COOKIE)
 * - Non-GET calls automatically carry `X-Moo-Admin: 1` (0.6.207-panel CSRF guard); the server
 *   answers 400 without it. GET/HEAD are exempt.
 * - SSE mode prints one line per data event and exits on done/error.
 * - NEVER point this at config.json or print credential fields.
 */
import process from 'node:process';

const argv = process.argv.slice(2);
const opts = { base: process.env.MOO_API_BASE || 'http://127.0.0.1:38101/api', cookie: process.env.MOO_COOKIE || '', raw: false, sse: false };
const args = [];
for (let i = 0; i < argv.length; i++) {
  const a = argv[i];
  if (a === '--base') opts.base = argv[++i];
  else if (a === '--cookie') opts.cookie = argv[++i];
  else if (a === '--raw') opts.raw = true;
  else if (a === 'sse') opts.sse = true;
  else args.push(a);
}

const [methodRaw, path, body] = args;
if (!methodRaw || !path || !path.startsWith('/')) {
  console.error('usage: moo-api.mjs [sse] <GET|POST|PUT|DELETE> <path> [json-body] [--base <url>] [--cookie <c>]');
  process.exit(2);
}
const method = methodRaw.toUpperCase();
const url = opts.base.replace(/\/$/, '') + path;
const headers = { Accept: 'application/json' };
if (opts.cookie) headers.Cookie = opts.cookie;
if (body) headers['Content-Type'] = 'application/json';
// 0.6.207-panel D2：非 GET 变更请求必须携带 X-Moo-Admin（应用层 CSRF 守卫），
// 缺失时服务端返回 400（不是 403）——读操作 GET/HEAD 豁免。
if (method !== 'GET' && method !== 'HEAD') headers['X-Moo-Admin'] = '1';

if (opts.sse) {
  headers.Accept = 'text/event-stream';
  const res = await fetch(url, { method, headers, body: body ?? undefined });
  if (!res.ok || !res.body) {
    const text = await res.text().catch(() => '');
    console.error(`SSE failed: HTTP ${res.status} ${text.slice(0, 400)}`);
    process.exit(1);
  }
  const reader = res.body.getReader();
  const dec = new TextDecoder();
  let buf = '';
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += dec.decode(value, { stream: true });
    let idx;
    while ((idx = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, idx).replace(/\r$/, '');
      buf = buf.slice(idx + 1);
      if (!line.startsWith('data:')) continue;
      const payload = line.slice(5).trim();
      if (!payload) continue;
      console.log(payload);
      let ev;
      try { ev = JSON.parse(payload); } catch { continue; }
      if (ev.step === 'done') { console.error(`[terminal] done: ${ev.message ?? ''}`); process.exit(0); }
      if (ev.step === 'error') { console.error(`[terminal] error: ${ev.error ?? 'unknown'}`); process.exit(1); }
    }
  }
  console.error('[sse] stream closed without terminal event — poll GET /api/operations for the real state');
  process.exit(3);
}

const res = await fetch(url, { method, headers, body: body ?? undefined });
const ctype = res.headers.get('content-type') || '';
if (ctype.includes('application/json')) {
  const data = await res.json().catch(() => null);
  if (!res.ok) {
    console.error(`HTTP ${res.status}: ${JSON.stringify(data ?? {})}`);
    process.exit(1);
  }
  console.log(opts.raw ? JSON.stringify(data) : JSON.stringify(data, null, 2));
} else {
  if (!res.ok) { console.error(`HTTP ${res.status} (non-JSON response)`); process.exit(1); }
  const buf = Buffer.from(await res.arrayBuffer());
  process.stdout.write(buf);
}
