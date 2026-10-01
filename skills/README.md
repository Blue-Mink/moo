# skills/ — Agent Skills for Moo

AI agent skills (Claude / QwenPaw style) for understanding, diagnosing, and safely operating Moo through its HTTP API.

## Layout

```
skills/moo/
├── SKILL.md                 # entry: four-plane core model, default workflow, hard rules
├── references/
│   ├── product-model.md     # what Moo is; data/operation/network/observability planes
│   ├── api-full.md          # full API documentation (mirror of docs/API.md)
│   ├── feature-map.md       # user-facing feature → API endpoint map
│   ├── install-deploy.md    # FPK build pipeline, install/hot-swap, release pipeline
│   ├── safety.md            # mutation tiers, credential rules, pre-publish sanitize scan
│   └── troubleshooting.md   # symptom-driven diagnosis
└── scripts/
    ├── moo-api.mjs          # thin API client (JSON + SSE iterator)
    └── moo-health.sh        # read-only status bundle
```

## Install into an agent workspace

```bash
cp -r skills/moo <agent-workspace>/skills/moo
```

Self-contained; no network needed at load time.

## Quick start

```bash
bash skills/moo/scripts/moo-health.sh http://<NAS_IP>:5666/app/moo/api
node skills/moo/scripts/moo-api.mjs GET /api/version --base http://<NAS_IP>:5666/app/moo/api
```

## Sanitization

This directory is fully sanitized: no real host addresses, credentials, tokens, or personal identifiers — placeholders only (`<NAS_IP>`, `<PANEL_PASSWORD>`, `<KEY>`). Keep it that way on updates (scan in `skills/moo/references/safety.md`).

Keep `skills/moo/references/api-full.md` in sync with `docs/API.md` when the API changes.

## Skills in this repo

| 技能 | 用途 |
|---|---|
| `skills/moo/` | **使用 / 诊断 Moo**：四平面核心模型、完整 HTTP API、功能→接口映射、安装与热替换、安全规则、排障 |
| `skills/fpk-build-to-moo/` | **发布到 Moo**：写 `moo.json`（协议见 `docs/MOO-PROTOCOL.md`）、用生成器产出索引、搜索框自测、添加为源与详情页验收、版本比较与更新判定、发布卫生（脱敏自检 `tests/test_docs.py`） |
