# Moo Safety

## Mutation Confirmation

### Request header contract (0.6.207-panel+)

Every **non-GET** call must send `X-Moo-Admin: 1` alongside `Content-Type: application/json`.
It is the application-layer CSRF guard (cross-site simple requests cannot add custom headers).
Missing it → **400** `缺少请求校验头（X-Moo-Admin）`, which means the request never reached the handler —
fix the header rather than re-trying blindly. Read-only GETs are exempt.
`scripts/moo-api.mjs` sets it automatically for non-GET methods.

When `moo.confirm_mutations` is true, before executing any non-read-only call:

1. Show the **method + path + exact JSON body**.
2. State the **effect and blast radius** (which plane, what is preserved, what is not).
3. State the **rollback** (what undoes it; note: FPK install has no API rollback — data volume survives, app package does not).
4. Wait for explicit user confirmation.

### Mutation Risk Tiers

| Tier | Operations | Default |
|---|---|---|
| R1 low | enable/disable source, rename, reorder, favorites, ignore-update, notify rules | show + quick confirm |
| R2 medium | add/delete source, settings writes (mirror, download dir, backup dir, proxy), backup restore, cache clean, channel CRUD | show + confirm with effect summary |
| R3 high | app install/update/uninstall, self-update, `POST /api/backups/clean {"force":true}`, delete backups, `panel_*` credential changes | show + confirm + recommend a fresh backup first |

R3 recommendations:
- Before any R3 op, optionally `POST /api/backups` (fresh config snapshot) and mention the backup name in the confirm prompt.
- Uninstall: verify the target `key` (mind the `@source` suffix on same-name apps) before firing.
- Self-update: confirm `GET /api/store-update` shows a newer version and that the release repo is the expected public one.

## Credentials & Secrets — What Never Leaves the Box

| Data | Where | Rule |
|---|---|---|
| fnOS panel password | `config.json` (`panel_password`), file mode 700 | **Never print, log, or transmit.** API only returns `panel_has_password: bool`. If the user wants to change it, use `PUT /api/settings {"panel_password":"<NEW>"}` and confirm via `POST /api/panel/test`. |
| Notify channel params | `config.json` (webhook URLs / keys) | Read endpoints return **masked** values. Re-POSTing a masked value back is a no-op-or-error — always send the real value or omit the field. |
| Proxy URL | `config.json` (`proxy_url`) | Local-only; never exfiltrate. It is not a credential, but it reveals the user's proxy topology. |
| Backup files | `backups/` (config snapshots **with** the panel password) | Download links are admin-gated; treat downloaded backup files as secrets. Do not upload them anywhere. |
| GitHub release token (build/release machine only) | build env / release tooling | Never embed tokens in the repo, FPK, docs, or skill files. |

## Channel Discipline

- All API work goes through the **trusted channel** (fnOS panel gateway `/app/moo/api/`). Direct-port calls from other hosts fail `requireAdmin` (403) by design — do not try to bypass by opening the listen port to the LAN.
- `requireAdmin` covers every write and every settings read. Public read endpoints exist for panel browsing — they must stay non-sensitive.
- The panel base URL is validated to be **loopback** at save time; a non-loopback `panel_base_url` is rejected.

## SSRF & Supply-Chain Guards (already in the backend — don't disable them)

- Source-provided URLs (icon/readme/preview) are fetched only after a public-address check (loopback/internal/metadata rejected).
- FPK install verifies sha256 before handing to appcenter.
- Self-update verifies the release asset hash; only amd64 FPKs are accepted.

## Operational Safety

- **/tmp is not durable** on many fnOS dev/test setups (tmpfs, reaped on reboot/cleanup). Never stage binaries, logs, or tools in /tmp across sessions; use the data dir or a /volN path.
- **pkill self-match**: from an SSH one-liner, `pkill -f pattern` matches the remote bash's own command line. Use `pkill -f "pat[tern]"`.
- **SSE ≠ cancel**: closing an SSE stream does not stop the op. Always confirm the terminal state (`GET /api/operations` by op ID, or `GET /api/tasks`) before declaring success.
- **Hot swap**: back up the old binary to a durable path first; verify `/api/version` after start.
- **Whole-request semantics**: settings writes reject atomically — a 400 means nothing was applied; fix the body and retry.
- **Empty-source auto-care** can disable sources it considers empty (`empty_streak` ≥ 5). After a bulk source change, watch `GET /api/sources` for unexpected `enabled:false`.

## Docs & Skills Hygiene (this repo)

Everything in this skill repo must stay **sanitized**:

- No real NAS IPs (use `<NAS_IP>`), no hostnames, no LAN topology.
- No passwords, webhook keys, tokens (use `<PANEL_PASSWORD>`, `<KEY>`, `<GH_TOKEN>` placeholders).
- No personal accounts, user IDs, or private repo URLs. Public product facts (port defaults, product name, public repo) are fine.
- Loopback examples (`127.0.0.1:7890`, `socks5://127.0.0.1:1080`) are acceptable as generic placeholders.

Before publishing: run a scan (see below).

### Pre-publish scan

```bash
# from the repo root: look for LAN IPs, tokens, keys, passwords
grep -rniE '10\.[0-9]+\.[0-9]+\.[0-9]+|192\.168\.[0-9]+\.[0-9]+|172\.(1[6-9]|2[0-9]|3[01])\.[0-9]+\.[0-9]+|ghp_[A-Za-z0-9]{20,}|xox[baprs]-|webhook/send\?key=[A-Za-z0-9_-]{8,}|password\s*[:=]\s*["'"'"'][^<"'"'"']+' . \
  | grep -v node_modules | grep -v '\.git/'
```

Any hit must be justified (public product fact) or redacted before publish.
