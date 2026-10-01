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
    print("折叠按钮数:", btns.count())
    if btns.count() > 0:
        btns.first.tap()
        page.wait_for_timeout(1500)

    info = page.evaluate("""() => {
      const out = [];
      document.querySelectorAll('*').forEach(e => {
        const cs = getComputedStyle(e);
        if (cs.overflowY === 'auto' || cs.overflowY === 'scroll') {
          out.push({
            cls: (e.className || '').toString().slice(0, 70),
            sh: e.scrollHeight, ch: e.clientHeight,
            sw: e.scrollWidth, cw: e.clientWidth,
            st: e.scrollTop, touchAction: cs.touchAction
          });
        }
      });
      return {
        docSh: document.documentElement.scrollHeight,
        innerH: window.innerHeight,
        bodyOverflow: getComputedStyle(document.body).overflow,
        scrollables: out,
      };
    }""")
    print("docSh/innerH:", info["docSh"], info["innerH"], "| body overflow:", info["bodyOverflow"])
    for s in info["scrollables"]:
        print("  scrollable:", s)

    target = page.evaluate("""() => {
      let best = null, bestH = 0;
      document.querySelectorAll('.overflow-y-auto').forEach(e => {
        const h = e.scrollHeight - e.clientHeight;
        if (h > bestH) { bestH = h; best = e; }
      });
      if (best) best.setAttribute('data-scrolltest', '1');
      return best ? {sh: best.scrollHeight, ch: best.clientHeight} : null;
    }""")
    print("主滚动容器:", target)

    page.evaluate("() => { const e = document.querySelector('[data-scrolltest]'); if (e) e.scrollTop = 500; }")
    page.wait_for_timeout(500)
    print("JS 滚动后 scrollTop:", page.evaluate("() => { const e = document.querySelector('[data-scrolltest]'); return e ? e.scrollTop : null; }"))
    page.evaluate("() => { const e = document.querySelector('[data-scrolltest]'); if (e) e.scrollTop = 0; }")
    page.wait_for_timeout(300)

    client = ctx.new_cdp_session(page)
    def swipe(y_start, y_end, x=195):
        client.send("Input.dispatchTouchEvent", {"type": "touchStart", "touchPoints": [{"x": x, "y": y_start}]})
        for i in range(1, 21):
            y = y_start + (y_end - y_start) * i / 20
            client.send("Input.dispatchTouchEvent", {"type": "touchMove", "touchPoints": [{"x": x, "y": y}]})
        client.send("Input.dispatchTouchEvent", {"type": "touchEnd", "touchPoints": []})
    swipe(600, 200)
    page.wait_for_timeout(1000)
    print("触摸上滑后 scrollTop:", page.evaluate("() => { const e = document.querySelector('[data-scrolltest]'); return e ? e.scrollTop : null; }"))
    page.screenshot(path="/tmp/mob_accel.png")
    b.close()
