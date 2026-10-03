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
    for _ in range(2):
        if btns.count() > 0:
            btns.first.tap()
            page.wait_for_timeout(1200)

    # 归零后取视口内数据行
    page.evaluate("() => { const e = document.querySelector('.overflow-y-auto.pt-3'); if (e) e.scrollTop = 0; }")
    page.wait_for_timeout(800)
    geom = page.evaluate("""() => {
      const sc = document.querySelector('.overflow-y-auto.pt-3');
      const rows = [...sc.querySelectorAll('div')].filter(e =>
        e.children.length >= 3 && /\\d+ms|失败|未测速/.test(e.textContent) && e.textContent.length < 60
      );
      const out = [];
      for (const r of rows) {
        const b = r.getBoundingClientRect();
        if (b.y > 60 && b.y < 780) out.push({t: r.textContent.trim().slice(0, 20), x: Math.round(b.x + 60), y: Math.round(b.y + b.height / 2)});
        if (out.length >= 3) break;
      }
      return {sc_st: sc.scrollTop, rows: out};
    }""")
    print("scrollTop:", geom["sc_st"], "| 视口内行:", geom["rows"])

    client = ctx.new_cdp_session(page)
    def swipe(x, y_start, y_end):
        client.send("Input.dispatchTouchEvent", {"type": "touchStart", "touchPoints": [{"x": x, "y": y_start}]})
        for i in range(1, 21):
            y = y_start + (y_end - y_start) * i / 20
            client.send("Input.dispatchTouchEvent", {"type": "touchMove", "touchPoints": [{"x": x, "y": y}]})
        client.send("Input.dispatchTouchEvent", {"type": "touchEnd", "touchPoints": []})

    for r in geom["rows"]:
        page.evaluate("() => { const e = document.querySelector('.overflow-y-auto.pt-3'); if (e) e.scrollTop = 0; }")
        page.wait_for_timeout(300)
        swipe(r["x"], r["y"], r["y"] - 350)
        page.wait_for_timeout(800)
        st = page.evaluate("() => { const e = document.querySelector('.overflow-y-auto.pt-3'); return e ? e.scrollTop : null; }")
        print(f"行「{r['t']}」@({r['x']},{r['y']}) 上滑 → scrollTop:", st)
    # 向下滑（回顶部）
    page.evaluate("() => { const e = document.querySelector('.overflow-y-auto.pt-3'); if (e) e.scrollTop = 0; }")
    page.wait_for_timeout(300)
    r = geom["rows"][1] if len(geom["rows"]) > 1 else geom["rows"][0]
    swipe(r["x"], r["y"] - 300, r["y"] + 200)
    page.wait_for_timeout(800)
    print("向下滑后 scrollTop:", page.evaluate("() => { const e = document.querySelector('.overflow-y-auto.pt-3'); return e ? e.scrollTop : null; }"))
    page.screenshot(path="/tmp/mob_accel3.png")
    b.close()
