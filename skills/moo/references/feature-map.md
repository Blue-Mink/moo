# Moo Feature → API Map

User-facing feature → the exact endpoints implementing it. Full request/response contracts in `api-full.md`.

## Browsing & Discovery

| Feature | Endpoints |
|---|---|
| App catalog (merged, ETag-cached) | `GET /api/apps` |
| App detail (+ install wizard schema) | `GET /api/apps/{key}` (`?wizard=1` for wizard) |
| Explore / recommended | `GET /api/recommended` |
| Installed apps (platform view) | `GET /api/installed`, `GET /api/installed-detail` |
| Search by source link (normalizes repo/raw/CDN/index forms) | `GET /api/search/source-keys` |
| App icon / preview / README (cached, SSRF-guarded) | `GET /api/apps/{key}/asset?type=icon\|preview&index=N\|readme&u=…` |

## Official App Center (fnOS official store, OAuth)

| Feature | Endpoints |
|---|---|
| Connection status (public) | `GET /api/official/status` |
| Start authorization — returns the URL to open | `GET /api/official/authorize` |
| Finish authorization with the one-time code | `POST /api/official/callback` |
| Disconnect (clear stored token) | `POST /api/official/logout` |
| Official store list (10-minute cache) | `GET /api/official/apps` |
| Search the official store | `GET /api/official/search?keyword=…` |
| Official app detail | `GET /api/official/apps/{name}` |

Upstream unreachable or not authorized → **502**; `status` only reports local state (never 502).

## Install / Update / Uninstall (all SSE, admin)

| Feature | Endpoints |
|---|---|
| Install | `POST /api/apps/{key}/install` |
| Update (data-preserving) | `POST /api/apps/{key}/update` |
| Uninstall | `POST /api/apps/{key}/uninstall` |
| Download-only task (pause/resume) | `POST /api/apps/{key}/download-task`, `POST /api/apps/{key}/task/pause`, `POST /api/apps/{key}/task/resume` |
| Check for updates (full source refresh + diff) | `POST /api/check` |
| Reload catalog | `POST /api/apps/reload` |
| Ignore / un-ignore updates | `PUT /api/apps/{key}/ignore-update`, `DELETE /api/apps/{key}/ignore-update` |
| Start / stop installed app | `POST /api/apps/{key}/start`, `POST /api/apps/{key}/stop` |
| Install wizard fields (standalone route; equivalent to `?wizard=1`) | `GET /api/apps/{key}/wizard` |
| Panel-side app detail (account / port / status, admin) | `GET /api/apps/{key}/panel-detail?appname=…` |
| Diagnostics (**501 not implemented**, M4 milestone) | `GET /api/apps/{key}/diagnostic` |
| Op progress recovery (after SSE disconnect) | `GET /api/operations`, `GET /api/tasks`, `DELETE /api/tasks/{id}` |

## Source Management (admin)

| Feature | Endpoints |
|---|---|
| List / add / batch-add (dedup) | `GET /api/sources`, `POST /api/sources`, `POST /api/sources/batch` |
| Reorder / rename / enable-disable / delete | `POST /api/sources/reorder`, `POST /api/sources/{id}/rename`, `POST /api/sources/{id}/toggle`, `DELETE /api/sources/{id}` |
| Refresh one / all (manual) | `POST /api/sources/{id}/sync`, `POST /api/sources/sync-all` |
| Restore default source list (fill missing + dedup, never removes official) | `POST /api/sources/restore-defaults` |
| Sync built-in source list | `POST /api/sources/sync-list` |
| Favorite (star) a source | `POST /api/sources/{id}/favorite` |

## Favorites (apps)

| Feature | Endpoints |
|---|---|
| List / toggle (cap 500, order = favorited order) | `GET /api/favorites`, `POST /api/favorites` |

## Settings (admin, pointer semantics — see api-full §11)

| Feature | Endpoints |
|---|---|
| Read / write all settings | `GET /api/settings`, `PUT /api/settings` |
| Download dir options / browse | `GET /api/settings/download-dirs`, `GET /api/settings/download-dirs/browse?path=…` |
| Mirror / Docker mirror selection + custom | via `PUT /api/settings` (`mirror`, `docker_mirror`, `custom_*`) |
| Auto speed-probe intervals | via `PUT /api/settings` (`gh_probe_*`, `dk_probe_*`) |
| Panel account | via `PUT /api/settings` (`panel_*`) + `POST /api/panel/test` |
| Auto-update apps | via `PUT /api/settings` (`auto_update`) |
| Dock / settings tab order (full permutation) | via `PUT /api/settings` (`dock_order`, `settings_tab_order`) |

## Scientific Acceleration (proxy, 0.6.206+)

| Feature | Endpoints |
|---|---|
| Enable + set proxy URL (applies immediately, no restart) | `PUT /api/settings` → `{"proxy_enabled": true, "proxy_url": "socks5://127.0.0.1:1080"}` |
| Test a candidate proxy (does NOT save, does NOT affect live config) | `POST /api/settings/proxy-test` → `{"ok": bool, "latency_ms": N, "error": "…"}` |
| Disable (URL kept for next enable) | `PUT /api/settings` → `{"proxy_enabled": false}` |

Rules: URL scheme must be http/https/socks4/socks5/socks5h with a host; enabled requires non-empty URL (whole-request reject otherwise); only GitHub domains route through the proxy; the URL never leaves the host.

## Backups & Cache (admin)

| Feature | Endpoints |
|---|---|
| List / create / delete / download / restore | `GET /api/backups`, `POST /api/backups`, `DELETE /api/backups/{name}`, `GET /api/backups/{name}/download`, `POST /api/backups/{name}/restore` |
| Clean app cache (icons/READMEs over age, or force-all) | `POST /api/backups/clean` → `{"days": 7}` or `{"force": true}` |

Backup file = full config snapshot **including credentials** — treat the file itself as sensitive.

## FPK Download Cache (admin)

| Feature | Endpoints |
|---|---|
| List / delete / install-from-cache (SSE) | `GET /api/fpk-downloads`, `DELETE /api/fpk-downloads/{name}`, `POST /api/fpk-downloads/{name}/install` |

## Acceleration Mirrors

| Feature | Endpoints |
|---|---|
| Health snapshot (GitHub group / Docker group) | `GET /api/mirrors/health`, `GET /api/mirrors/docker/health` |
| Manual re-probe | `POST /api/mirrors/check` |

## Notifications (admin unless noted)

| Feature | Endpoints |
|---|---|
| Channels CRUD + test (saved / draft) | `GET/POST /api/notify-channels`, `PUT/DELETE /api/notify-channels/{id}`, `POST /api/notify-channels/{id}/test`, `POST /api/notify-channels/test` |
| Rules & thresholds | `GET/PUT /api/notify-settings` |
| Log / append / clear | `GET/POST /api/notify-log`, `POST /api/notify-log/clear` |
| Manual fire / resend welcome | `POST /api/notify/fire`, `POST /api/notify/welcome` |
| Card detail page (one-time token, public) | `GET /api/notify-view/{id}?t=<token>` |

## Self-Update

| Feature | Endpoints |
|---|---|
| Has newer release? (red dot) | `GET /api/store-update` |
| In-place upgrade (SSE) | `POST /api/store-update` |

Note: only amd64; probe goes through the mirror chain; **release repo must be public** or the probe always 404s.

## System

| Feature | Endpoints |
|---|---|
| Version (+ trusted flag) | `GET /api/version` |
| Status / about | `GET /api/status`, `GET /api/about` |
| appcenter daemon reachable? | `GET /api/daemon/status` |
| Icon cache generation | `GET /api/icons/version` |
