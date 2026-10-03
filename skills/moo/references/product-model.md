# Moo Product Model

## What Moo Is

Moo (飞牛应用商店增强) is a self-hosted **community app store** panel for fnOS (飞牛 NAS). The official fnOS appcenter ships a curated store; Moo layers a bigger, community-maintained catalog on top:

- Aggregates many FPK sources (fndepot JSON indexes, conversun-style catalogs, the official store's source list) into one merged catalog with same-name multi-source coexistence (`appname@source`).
- Drives the fnOS **appcenter daemon** (local unix socket, loopback panel credentials) to install / update / uninstall / start / stop apps — always preserving the app's data volume (`@appdata`).
- Adds operational comfort: GitHub mirror acceleration with auto speed-selection, scientific-acceleration proxy, pause/resume downloads, ignore-update, favorites, config backups, 7-channel notifications, and in-app self-update (FPK in-place upgrade).

Moo itself ships as an FPK and runs as a single Go binary + static React frontend, served on its own web port (default 38101, `MOO_WEB_PORT` overridable) and proxied through the fnOS panel at `/app/moo/`.

## The Four Planes

Diagnose by plane first — most "Moo bugs" are one plane misbehaving while the others are fine.

### 1. Data plane
- `config.json` (mode 700) in the data dir: sources, settings, favorites, notify channels/rules/log, local install counters. **Contains the fnOS panel password — never print or transmit it.**
- `cache/`: catalog snapshot (seconds-cold start), icon cache, README cache — auto-cleaned by age policy (`cache_clean_days` / `cache_clean_every_days`).
- Catalog merge: each enabled source is fetched (fndepot index or directory listing) and merged; same `appname` from multiple sources coexists with `@source` key suffix.
- Catalog guard: a round where entries drop >20% fires `catalog_drop` (source-data anomaly signal).

### 2. Operation plane
- Long ops (install/update/uninstall/download/self-update) run as a **state machine**: one `current` op + bounded `history`; running→terminal is an atomic swap.
- SSE streams progress at ~700ms poll; the terminal state is **retrieved from history by op ID** — a client that sees "current is nil" must NOT assume success (the op may have just flipped to error in that window).
- Client disconnect does **not** cancel the op; it continues in background. Re-attach via `GET /api/operations` or `GET /api/tasks`.
- Install pipeline: download FPK → sha256 verify → appcenter install/upgrade (data-preserving). Official apps may take the platform cloud channel; community apps take the FPK pipeline.

### 3. Network plane (mirrors ⊥ proxy)
Two **orthogonal** mechanisms — the single most-asked question ("do they conflict?"):

| | Acceleration mirror (加速源) | Scientific acceleration (科学加速) |
|---|---|---|
| Level | URL: which address to fetch | Transport: how the connection is made |
| Applies to | gh-proxy / jsDelivr / Fastly / direct / custom prefixes | **Only** github.com + `*.github.com` / `*.githubusercontent.com` / `*.github.dev` / `*.githubassets.com` |
| Proxy interplay | mirror traffic is **always direct** (mirror host ≠ GitHub host) | the direct candidate (and its fallback) rides the proxy |

- Mirror auto-selection: periodic speed probes (default 5 min, `gh_probe_*` / `dk_probe_*` gears), switch fires `mirror_switched` (30-min cooldown). Probes stay direct on purpose (they measure mirror quality; a proxy would poison the comparison).
- All candidates failing → `mirrors_all_failed` (Docker group has `docker_mirrors_all_failed`); recovery → `*_recovered`.
- Proxy (0.6.206): `proxy_enabled` + `proxy_url` (http/https/socks4/socks5/socks5h). Applied at runtime (dynamic transport, no restart). Proxy down → direct candidate fails → mirror fallback chain takes over; nothing is hard-blocked.
- Egress points riding the proxy: source sync, icon fetch, README fetch, FPK download (community), self-update probe + FPK download.

### 4. Observability plane
- Channels: wecom | dingtalk | feishu | serverchan | pushplus | bark | webhook — params schema'd, **masked on read**, draft-test endpoint for unsaved configs.
- Rules: 30 event keys in 5 groups; semantics = **key missing → catalog default, only explicit false is off**; off events are neither pushed nor logged.
- Records: persistent log with per-channel send results, capped; in-app top notification bar is local feedback only (not a push channel).
- Card format (`format=card`) needs `view_base` (the panel URL where `/api/notify-view/{id}?t=<token>` resolves); unset → auto-degrades to markdown.

## Runtime Directories (fnOS)

| Path | Contents | Upgrade behavior |
|---|---|---|
| `/vol1/@appcenter/moo/` | `moo-server` binary + `web/` frontend | overwritten by FPK upgrade |
| `/vol1/@appdata/moo/` | `config.json`, `cache/`, `backups/`, `moo.log` | **preserved** (data volume) |

Env: `MOO_WEB_PORT` (listen port, default 38101); `TRIM_PKGVAR` (data dir root injected by the fnOS app framework).

## Background Loops (resident in-process)

| Loop | Cadence | Notes |
|---|---|---|
| Source auto-sync | `check_interval_hours` (default 24h) | concurrency 8 |
| Source auto-care | per sync round | auto-disable after 5 consecutive empty successful rounds; empty sources sink |
| GitHub mirror probe | `gh_probe_*` (default 5m) | auto-select |
| Docker mirror probe | `dk_probe_*` (default 5m) | includes local KSpeeder cache reference |
| Self-update probe | periodic + manual "check now" | GitHub Releases via mirror chain; **private repo → probe always 404 → red dot never lights** |
| Auto-update apps | per sync round | only when `auto_update` on; excludes Moo itself (self-update loop guard) |
| Auto backup | `backup_interval_days` (default 7d) | full config snapshot |
| Cache clean | `cache_clean_every_days` | age-based, never touches downloaded FPKs or installed apps |
| Health self-check | 5m | daemon RPC reachability; `health_error` / `health_recovered` |
| Resource alerts | 30m cooldown | mem >256MB / CPU >80% sustained 5min |
| Disk alert | 30m cooldown | data dir usage >90% |

## Trust Boundary

- The panel gateway (fnOS appcenter web proxy) is the **trusted channel**: it marks requests and carries the admin session. Write endpoints = trusted + admin, else 403.
- The panel account Moo stores (`panel_*`) is used **only** against loopback `http://127.0.0.1:5666` for six appcenter endpoints; `panel_base_url` is validated to be loopback at save time.
- Source-provided URLs (icon/readme/preview) are SSRF-guarded: only public addresses may be fetched.
