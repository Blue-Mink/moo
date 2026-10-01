---
name: moo
description: Understand, diagnose, and safely operate Moo, a self-hosted community app store for fnOS/Fn Connect (飞牛NAS) that aggregates third-party FPK app sources, provides install/update/uninstall with data preservation, mirror acceleration, notification channels, and in-app self-update. Use for questions about Moo concepts, the HTTP API (78 endpoints), FPK build/install/hot-swap, acceleration mirrors (gh-proxy/jsDelivr/Fastly/direct) and scientific-acceleration proxy, notification channels, backups, troubleshooting (source sync failures, icon 404s, self-update red dot, /tmp tmpfs pitfalls), and safe configuration changes via the settings API.
version: 1.0.0
metadata:
  hermes:
    tags: [moo, fnos, fnos-nas, app-store, fpk, devops]
    category: devops
    config:
      - key: moo.api_base_url
        description: Base URL of the Moo HTTP API (via fnOS panel gateway, trusted channel recommended).
        default: http://<NAS_IP>:5666/app/moo/api
        prompt: Moo API base URL
      - key: moo.panel_base_url
        description: fnOS panel base URL (loopback; Moo calls appcenter daemon locally via panel credentials).
        default: http://127.0.0.1:5666
        prompt: fnOS panel base URL
      - key: moo.confirm_mutations
        description: Require user confirmation before non-read-only Moo operations (install/update/uninstall/settings writes).
        default: true
        prompt: Confirm Moo mutations before execution
---

# Moo Agent Skill

Use this skill to understand what Moo is, decide how to diagnose it, and operate a local Moo instance through its documented HTTP API.

Moo is a **community app store panel** for fnOS: it aggregates FPK app sources (fndepot JSON indexes, conversun-style catalogs, official store lists), merges them into one catalog, and drives the fnOS appcenter daemon to install/update/uninstall apps while preserving user data (`@appdata`). It adds GitHub mirror acceleration, a scientific-acceleration proxy (0.6.206+), multi-channel notifications, config backups, and in-app self-update.

## Core Model

Treat Moo as **four planes** that interact:

1. **Data plane** — source catalog (merged FPK indexes), icon/README cache, config.json (700, holds panel credentials — never print it).
2. **Operation plane** — long-running ops (install/update/uninstall/download) run as a state machine (`current` + `history`); SSE streams progress; disconnect does NOT cancel the op.
3. **Network plane** — acceleration mirrors (URL level: gh-proxy/jsDelivr/Fastly/direct, auto-selected by speed) are **orthogonal** to the scientific-acceleration proxy (transport level: only github.com and its subdomains route through the local proxy; mirrors always direct).
4. **Observability plane** — notify channels (wecom/dingtalk/feishu/serverchan/pushplus/bark/webhook) × 30 event rules × persistent log; in-app notification bar is local-only.

Load `references/product-model.md` when the user asks what Moo is, which plane a problem belongs to, or how the pieces fit.

## Default Workflow

1. Identify the target: concept explanation, read-only inspection, diagnosis, or configuration change.
2. Determine the **channel**: API calls should go through the fnOS panel gateway (`/app/moo/api/`) so requests are trusted + admin. Direct port access works on the NAS itself but write endpoints need the trusted channel.
3. Load `references/api-full.md` for the exact endpoint contract before calling anything (78 endpoints, request/response schemas, SSE protocol).
4. Prefer the smallest read workflow; for diagnosis, load `references/troubleshooting.md` first and match symptoms.
5. Use `references/feature-map.md` to map a user-facing feature (favorite, ignore-update, proxy, backups…) to its API endpoints.
6. For build/deploy questions, load `references/install-deploy.md` (repo layout, build pipeline, hot-swap, runtime directories).
7. Before any write, delete, install, restart, or settings change: load `references/safety.md`, show the method/path/body, explain the effect, and wait for confirmation when `moo.confirm_mutations` is true.
8. After mutations, verify with a read endpoint (see verification loops in `references/api-full.md` §19/§20).

## Reference Routing

- What Moo is, the four planes, runtime directories, background loops: `references/product-model.md`.
- Full API contract (all 78 endpoints, types, SSE, errors, security model, build flow): `references/api-full.md`.
- Feature → API map (sources, favorites, ignore-update, mirrors, proxy, notifications, backups, self-update): `references/feature-map.md`.
- FPK build, install, hot-swap, release pipeline, env vars: `references/install-deploy.md`.
- Mutation confirmation, credential handling, what never to exfiltrate: `references/safety.md`.
- Symptom-driven diagnosis (sync failures, icon 404, red dot, /tmp tmpfs, proxy refused): `references/troubleshooting.md`.

## API Helpers

### Single-endpoint client

`scripts/moo-api.mjs` — thin fetch wrapper for the Moo API (trusted-channel aware, SSE iterator included).

```bash
node moo/scripts/moo-api.mjs get /api/version --base http://<NAS_IP>:5666/app/moo/api
node moo/scripts/moo-api.mjs post /api/sources/sync-all --base <base>
node moo/scripts/moo-api.mjs sse post /api/apps/<key>/install --base <base>
```

### Health scan

`scripts/moo-health.sh` — read-only status bundle (version, daemon, mirror health, tasks, ops) in one call; safe to run unattended.

```bash
bash moo/scripts/moo-health.sh http://<NAS_IP>:5666/app/moo/api
```

## Hard Rules

- **Never** print or transmit `config.json` contents (it holds the fnOS panel password). Use `panel_has_password` booleans and masked channel params.
- **Never** call write endpoints without the trusted channel (panel gateway) — expect 403 otherwise.
- **Never** treat an SSE client disconnect as op success — poll `GET /api/operations` or `GET /api/tasks` to confirm the terminal state by op ID.
- **Never** assume `/tmp` persists on fnOS test/dev boxes (tmpfs may be reaped); keep tooling artifacts under the data dir.
- **Never** use `pkill -f <pattern>` from an SSH one-liner where the pattern also appears in the remote command line (self-match kills your own shell) — use the bracket trick: `pkill -f "pat[tern]"`.
- Proxy config is local-only: the proxy URL is stored in config.json and never sent anywhere; only GitHub domains route through it.
