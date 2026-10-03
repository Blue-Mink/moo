import os
from playwright.sync_api import sync_playwright

BASE = os.environ.get("MOO_BASE", "http://127.0.0.1:38120")  # 脱敏：默认回环，export MOO_BASE 注入实际入口

with sync_playwright() as p:
    b = p.chromium.launch(headless=True)
    ctx = b.new_context(viewport={"width": 390, "height": 844}, has_touch=True, is_mobile=True)
    page = ctx.new_page()
    page.goto(BASE + "/", wait_until="domcontentloaded", timeout=30000)
    page.wait_for_timeout(6000)
    page.get_by_role("button", name="设置").tap()
    page.wait_for_timeout(2000)
    page.get_by_role("tab", name="加速设置").tap()
    page.wait_for_timeout(2500)
    btns = page.get_by_role("button", name="展开监测的加速源列表")
    if btns.count() > 0:
        btns.first.tap()
        page.wait_for_timeout(1500)
    # 第二个也展开（Docker 列表）
    btns = page.get_by_role("button", name="展开监测的加速源列表")
    if btns.count() > 0:
        btns.first.tap()
        page.wait_for_timeout(1500)

    # 找到 GitHub 列表第一行和数据行的位置
    geom = page.evaluate("""() => {
      const rows = [...document.querySelectorAll('.overflow-y-auto.pt-3 *')].filter(e =>
        e.children.length === 0 && /ms/.test(e.textContent || '') && /\\d+ms/.test(e.textContent)
      );
      const out = rows.slice(0, 4).map(r => {
        const b = r.getBoundingClientRect();
        return {t: r.textContent.trim(), x: Math.round(b.x + b.width/2), y: Math.round(b.y + b.height/2)};
      });
      // 元素遮挡检查
      const pts = out.map(o => [o.x, o.y]);
      const cover = pts.map(([x, y]) => {
        const els = document.elementsFromPoint(x, y);
        return els.slice(0, 4).map(e => (e.tagName + '.' + (e.className||'').toString().slice(0, 40)));
      });
      return {rows: out, cover};
    }""")
    print("数据行:", geom["rows"])
    for c in geom["cover"]:
        print("  遮挡链:", c)

    client = ctx.new_cdp_session(page)
    def swipe(x, y_start, y_end):
        client.send("Input.dispatchTouchEvent", {"type": "touchStart", "touchPoints": [{"x": x, "y": y_start}]})
        for i in range(1, 21):
            y = y_start + (y_end - y_start) * i / 20
            client.send("Input.dispatchTouchEvent", {"type": "touchMove", "touchPoints": [{"x": x, "y": y}]})
        client.send("Input.dispatchTouchEvent", {"type": "touchEnd", "touchPoints": []})

    # 在每个数据行位置分别上滑
    for r in geom["rows"]:
        page.evaluate("() => { const e = document.querySelector('.overflow-y-auto.pt-3'); if (e) e.scrollTop = 0; }")
        page.wait_for_timeout(300)
        x, y = r["x"], r["y"]
        swipe(x, y, y - 350)
        page.wait_for_timeout(800)
        st = page.evaluate("() => { const e = document.querySelector('.overflow-y-auto.pt-3'); return e ? e.scrollTop : null; }")
        print(f"行「{r['t']}」({x},{y}) 上滑后 scrollTop:", st)
    page.screenshot(path="/tmp/mob_accel2.png")
    b.close()
