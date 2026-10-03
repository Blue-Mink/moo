"""SW 端到端验证（真实浏览器，内网入口经 MOO_BASE 注入，复用已登录 cookie）：
1. 打开 Moo 应用列表（首载：图标走网络）
2. 再次加载：拦截(abort)全部图标网络请求
   → 图标仍渲染 = SW 缓存真实生效（铁证）
"""
import os, sys
from playwright.sync_api import sync_playwright

BASE = os.environ.get("MOO_BASE", "https://127.0.0.1:5667")  # 脱敏：默认回环

def count_rendered(page, settle_ms=3000):
    page.wait_for_timeout(settle_ms)
    imgs = page.locator("img")
    n = imgs.count()
    vis = ok = 0
    for i in range(n):
        try:
            bb = imgs.nth(i).bounding_box()
            if not bb or not (0 <= bb["y"] <= 900):
                continue
            vis += 1
            if imgs.nth(i).evaluate("el => el.complete && el.naturalWidth > 0"):
                ok += 1
        except Exception:
            pass
    return vis, ok

with sync_playwright() as p:
    browser = p.chromium.launch(headless=True, args=["--ignore-certificate-errors"])
    ctx = browser.new_context(
        viewport={"width": 1280, "height": 800},
        ignore_https_errors=True,
        storage_state="/tmp/fnos_state.json",
    )
    page = ctx.new_page()

    # ---- 首次加载（图标走网络）----
    icon_reqs = []
    page.on("request", lambda r: icon_reqs.append(r.url)
            if "/asset?type=icon" in r.url else None)
    page.goto(BASE + "/app/moo/#/apps", wait_until="domcontentloaded")
    vis1, ok1 = count_rendered(page)
    reg = page.evaluate(
        "() => navigator.serviceWorker.getRegistration().then(r => r ? (r.active ? 'active' : r.waiting ? 'waiting' : 'installing') : 'none')"
    )
    cache_keys = page.evaluate(
        "async () => { try { return (await caches.keys()).filter(k => k.startsWith('moo-icons')); } catch { return ['no-cache-api']; } }"
    )
    print(f"[1] 首载: 视口图标 {ok1}/{vis1} 渲染, 图标网络请求 {len(icon_reqs)} 个")
    print(f"    SW: {reg}, 页面侧 caches: {cache_keys}")
    if reg == "none":
        print("RESULT: FAIL — SW 未注册（检查 https/安全上下文）")
        browser.close()
        sys.exit(1)

    # ---- 再次加载：abort 全部图标网络请求 ----
    blocked = [0]
    def block(r):
        blocked[0] += 1
        r.abort()
    page.route("**/app/moo/api/apps/**/asset?type=icon**", block)
    page.reload(wait_until="domcontentloaded")
    vis2, ok2 = count_rendered(page)
    print(f"[2] 图标网络全封锁后重载: 视口图标 {ok2}/{vis2} 渲染 (拦截 {blocked[0]} 个请求)")
    page.screenshot(path="/tmp/sw_e2e_proof.png")
    print("[3] 截图: /tmp/sw_e2e_proof.png")

    if ok2 >= max(1, vis2 - 1):
        print("\nRESULT: PASS — SW 缓存真实生效（网络封锁下图标仍全渲染）")
    else:
        print(f"\nRESULT: FAIL — 封锁后仅 {ok2}/{vis2} 渲染")
        sys.exit(1)
    browser.close()
