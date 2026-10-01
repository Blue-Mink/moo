# Moo Troubleshooting

Symptom → likely cause → check → fix. Start with the **status bundle**:

```bash
bash moo/scripts/moo-health.sh http://<NAS_IP>:5666/app/moo/api
```

or manually: `GET /api/version`, `GET /api/daemon/status`, `GET /api/operations`, `GET /api/tasks`, `GET /api/mirrors/health`, `GET /api/sources`.

## 1. Install / Update Fails

**Check order:**
1. `GET /api/daemon/status` → `available:false` = appcenter daemon RPC unreachable → the fnOS appcenter service itself is down; Moo cannot install anything (health self-check pushes `health_error`).
2. SSE terminal event: `error` with the real reason. **If the SSE stream just closed with no done/error, the op may have failed in the running→error window** — poll `GET /api/operations` and read the terminal state from `history` **by op ID**. Never treat "current is nil" as success.
3. sha256 mismatch at verify → the downloaded FPK is corrupted or the source index lists a stale hash → re-sync the source (`POST /api/sources/{id}/sync`) and retry; if persistent, the **source data is bad** (stale index), not your network.
4. Same-name app across sources: confirm you targeted the right `key` (`appname@source`). Updating the wrong twin is the classic silent mistake.
5. Platform-side refusal (rsync/permission errors from appcenter): often a **leftover symlink or stale appconf residue** from a prior broken install — needs manual cleanup on the NAS (stop app, inspect `/vol1/@appconf/<appname>` and `/vol1/@appcenter/<appname>`), then retry.

## 2. "No Update" / Red Dot Never Lights

- **Private release repo**: the probe hits the GitHub Releases API; a private repo returns 404 on every probe → `has_update:false` forever. **Make the repo public** (or use a mirror that serves it).
- Non-amd64 host: self-update is intentionally unavailable (official FPKs are x86-only); `/api/store-update` says so.
- Probe chain all failing (network layer): `GET /api/mirrors/health` — if `mirrors_all_failed` fired, GitHub is unreachable from this box via all mirrors; the red dot can't work offline.
- Proxy enabled but down: the direct candidate rides the proxy; if the proxy is dead and mirrors are exhausted, probe fails → check `proxy_url` liveness with `POST /api/settings/proxy-test`.

**Fake-update red dot** (a version appears "newer" but isn't): a source index listed a version that doesn't exist upstream. Verify the tag exists on the upstream; fix = correct the source index (upstream) or disable the bad source. A deliberate fake probe (e.g. pointing the probe at a local mirror) is the standard **E2E test** for the red-dot + notify chain — clean up the fake mirror afterwards and confirm `has_update:false` returns.

## 3. Source Sync Failures

- `GET /api/sources` → the failing source's `error` field is the ground truth (timeout / 404 / invalid JSON / DNS).
- DNS timeouts to `raw.githubusercontent.com`-style hosts (common in CN): expected when there's no working mirror/proxy — enable a mirror (`mirror != direct`) and/or enable the proxy. Mirror traffic is direct; only the GitHub direct candidate uses the proxy.
- Source returns 200 but 0 apps: `empty_streak` climbs; after 5 consecutive empty rounds the auto-care loop **disables and sinks** the source. If the source is legitimately empty sometimes, disable auto-care (`source_auto_care_disabled:true`) or fix the source.
- Catalog suddenly small: `catalog_drop` event fired (>20% drop) → one or more major sources failed that round; check per-source errors.
- Bulk paste with duplicates: `POST /api/sources/batch` dedups by URL and reports skipped count — if the count looks wrong, some "different" sources actually share the canonical URL (mirror forms of the same repo normalize to one key; see `GET /api/search/source-keys`).

## 4. Icons / README Missing or 404

- Icon fetch is SSRF-guarded: non-public `icon_url` values are **silently skipped** (log: "跳过（安全策略）"). A source pointing icons at an intranet address will show placeholders by design.
- Cross-source same-name icons: each `key` (with `@source` suffix) has its own cache entry — a new source for a known app may briefly show a stale/placeholder icon until the race-fetch (mirror vs direct) wins.
- 403/404/410 from the remote is treated as "definitely missing" (placeholder + cross-validation), distinct from network errors (retry next round).
- Stale cache after upstream fix: bump the icon cache generation (`GET /api/icons/version` before/after) or clean: `POST /api/backups/clean {"force":true}` (only touches the cache dir — never FPKs or installed apps).

## 5. Scientific Acceleration (Proxy)

- `connection refused` from proxy-test: the proxy software isn't listening (check the proxy app's ports first). Loopback addresses in the config must be reachable **from the Moo process** (same host) — a proxy on another LAN box needs that box's IP.
- Proxy enabled but source sync still slow/failing: verify what actually routed — a logging proxy that records `CONNECT github.com` lines is the ground truth; mirror domains must NOT appear (they stay direct by design).
- Proxy changed at runtime: no restart needed (dynamic transport applies on next request).
- socks5h (remote DNS) is supported for Clash-style mixed ports.
- If the proxy is intermittently dead: the mirror fallback chain covers it; expect sporadic `source_sync_failed` notifications, not a full outage.

## 6. NAS / Shell Pitfalls (deploy & debugging)

- **/tmp is tmpfs** on many fnOS test boxes — reaped on reboot or by cleanup. Binary backups, logs, and helper scripts must live under `/volN` (e.g. the data dir or a dedicated share). A "missing" /tmp artifact is almost always reclamation, not a bug.
- **pkill self-match**: `ssh host 'pkill -f foo'` kills the remote shell itself (its command line contains "foo") → SSH exits 255. Use `pkill -f "fo[o]"`.
- **Backgrounding over SSH**: `ssh host 'setsid nohup cmd &'` can hang the SSH session (the `&` keeps the channel open). Split: start in one call (or redirect all streams + `disown`), verify listening state in a second call.
- **Disk failure at inopportune times**: losing the local git clone means losing per-version history — sync to the remote repo **before** destructive local operations; commit messages should state when history was reconstructed from a loss event.
- **Reboots clear runtime state**: after a NAS reboot, re-verify: service started, `/tmp` artifacts gone, proxy process gone (re-start if needed), panel session re-login.

## 7. Frontend / Panel Issues

- "Not trusted" / 403 on writes: you're calling the raw port from a non-trusted context. Use the panel gateway URL (`http://<NAS_IP>:5666/app/moo/api/…`) with an admin session.
- UI shows stale data after a hot swap: hard-refresh the SPA (asset hashes changed); the backend is authoritative.
- Settings save "did nothing": pointer semantics — a field you think you set was actually **absent** from the body (unsubmitted = no change). Check the request body; explicit empty string = clear, absent = keep.
- Panel password can't be read back: by design (`panel_has_password` only). To rotate: send the new value in `PUT /api/settings` + `POST /api/panel/test`.

## 8. Notifications

- Channel test fails from the dialog but works after save: draft-test uses the exact body you typed (common: trailing spaces, missing key param). Saved-channel test uses stored params.
- Card notifications render as markdown: `view_base` unset → auto-degrade. Set `view_base` to the panel URL so `/api/notify-view/{id}?t=…` resolves.
- Event "didn't push": check the rule (explicit false = off; missing = default), the channel enabled flag, and the log (`GET /api/notify-log`) — off events are neither pushed **nor logged**.
- Duplicate-push fatigue: several events have 30-min cooldowns (`mirror_switched`, `mirrors_all_failed`, resource/disk alerts); if you still get spam, it's usually the **recovery→fail ping-pong** — find the flapping dependency (mirror host, proxy, daemon).

## 9. Self-Update Pipeline

- `已有自更新任务在进行中` (already running): a previous task is stuck — check `GET /api/operations`; the stuck task usually needs a service restart.
- Verify failed: sha256 mismatch between the release asset and the manifest → the release was mis-attached; re-upload the correct FPK to the release (byte-identical re-read check after upload).
- After a successful in-place upgrade: confirm with `/api/version`, then verify the red dot cleared (`GET /api/store-update` → `has_update:false`).

## 10. Log Locations

| Log | Path |
|---|---|
| Moo service log | data dir `moo.log` (e.g. `/vol1/@appdata/moo/moo.log`) |
| API audit (non-GET requests) | service stderr (captured by appcenter; also visible in `moo.log` on newer versions) |
| Notify dispatch results | `config.json` `notify_log` (also via `GET /api/notify-log`) |

Grep pattern for a deploy session: `grep -a "科学加速\|proxy\|sync\|race" moo.log | tail -50` (adjust to your locale).
