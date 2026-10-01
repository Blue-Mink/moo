# Moo Install / Deploy / Build

All commands below use placeholders. Run them on a build machine with Node 22+ and Go 1.25+; `fnpack` is the fnOS packaging CLI (from the fnOS app toolchain).

## Repo Layout

```
moo/
├── cmd/server/          # Go entry: config load → proxy restore → source manager → HTTP server
├── internal/
│   ├── api/             # all 78 endpoints + SSE + catalog/icon/readme caches
│   ├── config/          # config.json schema + load/save (atomic, 700 perms)
│   ├── source/          # source sync engines (fndepot index / conversun / official list)
│   ├── netx/            # scientific-acceleration proxy transport (0.6.206+)
│   ├── netguard/        # SSRF public-address guard
│   ├── notify/          # 7-channel push fanout
│   ├── operation/       # long-op state machine (current/history atomic swap)
│   ├── pipeline/        # install/update/uninstall pipeline (download→verify→appcenter)
│   └── platform/        # fnOS appcenter daemon RPC (unix socket, loopback)
├── frontend/            # React + Vite (build output → web/)
├── fnos/                # FPK descriptor: manifest + lifecycle scripts
├── web/                 # built frontend (shipped inside the FPK)
└── docs/API.md          # full API documentation
```

## Building the FPK (x86)

```bash
# 1) version bump (keep in lockstep!)
#    - fnos/manifest  →  version = 0.6.206
#    - README.md      →  badge   →  FPK-0.6.206

# 2) frontend (output → web/)
cd frontend && npm run build            # tsc -b && vite build

# 3) backend (version injected via -X main.Version)
export PATH=<go-install>/bin:$PATH
GOOS=linux GOARCH=amd64 go build \
  -ldflags "-X main.Version=0.6.206" \
  -o build/moo-server-x86 ./cmd/server/

# 4) staging
STAGING=build/tmp/fnos-x86
rm -rf "$STAGING"
cp -a fnos "$STAGING"
cp build/moo-server-x86 "$STAGING/app/moo-server"
cp -r web "$STAGING/app/web"

# 5) pack
./build/fnpack build --directory "$STAGING"
mv moo.fpk moo_0.6.206_x86.fpk
sha256sum moo_0.6.206_x86.fpk         # record the sha for the release note
```

Build gotchas:
- **Go toolchain drift**: `go get <new-dep>@latest` may bump the `go` directive / download a newer toolchain. Prefer pinning deps to versions compatible with your current `go` directive; keep the toolchain line stable.
- Frontend types: the settings contract is mirrored between `internal/api/store_types.go` (Go) and `frontend/src/api/client.ts` (TS) — a new settings field needs both sides or `tsc -b` fails.

## Installing / Upgrading on the NAS

Standard path (fnOS UI or CLI):

```bash
# install (or upgrade if already present — @appdata is preserved)
appcenter-cli install /vol1/downloads/moo_0.6.206_x86.fpk

appcenter-cli stop moo
appcenter-cli start moo
curl -s http://127.0.0.1:38101/api/version   # {"version":"0.6.206","trusted":…}
```

Runtime locations after install:

| Path | Contents | Upgrade |
|---|---|---|
| `/vol1/@appcenter/moo/` | binary + web/ | replaced by the new FPK |
| `/vol1/@appdata/moo/` | config.json (700), cache/, backups/, moo.log | **preserved** |

## Hot Swap (dev loop, no FPK)

```bash
appcenter-cli stop moo
cp build/moo-server-x86 /vol1/@appcenter/moo/moo-server
rm -rf /vol1/@appcenter/moo/web
cp -r web /vol1/@appcenter/moo/web
appcenter-cli start moo
sleep 5
curl -s http://127.0.0.1:38101/api/version
```

Hot-swap rules:
- Always back up the previous binary first (`cp moo-server backup-moo-server-<ver>`) — the data dir is the safe harbor, not /tmp.
- Verify with `/api/version` before touching the UI.
- A panel-login state survives hot swap; the app SPA may need a refresh.

## Release Pipeline (GitHub)

1. Source sync to the release repo (commit message: one-line highlight per version).
2. Push `main`.
3. Tag `v0.6.206` + create Release with:
   - one-line bold highlight + one-sentence description (≤30 chars style),
   - FPK asset attached,
   - FPK sha256 tail line (e.g. `📦 moo_0.6.206_x86.fpk · sha256 <16-hex-prefix>`).
4. **Repo must be public** for the self-update red dot to work (private → probe always 404).

## Environment Variables

| Var | Default | Meaning |
|---|---|---|
| `MOO_WEB_PORT` | `38101` | listen port for the standalone API/UI |
| `TRIM_PKGVAR` | (framework-injected) | data dir root (@appdata absolute path) |

## Lifecycle Scripts (fnos/)

- install: extract package, start service.
- upgrade: stop → replace binary/web → start (config.json untouched).
- uninstall: stop service; data volume retained by default so a reinstall restores state.

## Verification Checklist (post-deploy)

```bash
curl -s http://127.0.0.1:38101/api/version           # version + trusted
curl -s http://127.0.0.1:38101/api/daemon/status     # appcenter RPC reachable
curl -s http://127.0.0.1:38101/api/operations        # current op + history
# via panel gateway (trusted channel):
curl -s http://<NAS_IP>:5666/app/moo/api/settings | head -c 200
```
