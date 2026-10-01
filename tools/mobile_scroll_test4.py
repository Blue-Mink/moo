import os
from playwright.sync_api import sync_playwright

BASE = os.environ.get("MOO_BASE", "http://127.0.0.1:38120")  # 脱敏：默认回环，export MOO_BASE 注入实际入口

def run(p, width, height, tag):
    b = p.chromium.launch(headless=True)
    ctx = b.new_context(viewport={"width": width, "height": height}, has_touch=True, is_mobile=width < 500)
    page = ctx.new_page()
    page.goto(BASE + "/", wait_until="domcontentloaded", timeout=30000)
    page.wait_for_timeout(6000)
    page.get_by_role("button", name="设置").tap()
    page.wait_for_timeout(1800)
    page.get_by_role("tab", name="加速设置").tap()
    page.wait_for_timeout(2000)
    btns = page.get_by_role("button", name="展开监测的加速源列表")
    for _ in range(2):
        if btns.count() > 0:
            btns.first.tap()
            page.wait_for_timeout(1000)
    info = page.evaluate("""() => {
      const dlg = document.querySelector('[role=dialog]');
      const cs = getComputedStyle(dlg);
      const sc = document.querySelector('.overflow-y-auto.pt-3');
      const scs = getComputedStyle(sc);
      const dlgRect = dlg.getBoundingClientRect();
      // 检查是否有子元素溢出 dialog 边界
      let maxRight = 0, maxBottom = 0, minLeft = 1e9, minTop = 1e9;
      dlg.querySelectorAll('*').forEach(e => {
        const r = e.getBoundingClientRect();
        if (r.width > 0 && r.height > 0) {
          maxRight = Math.max(maxRight, r.right);
          maxBottom = Math.max(maxBottom, r.bottom);
          minLeft = Math.min(minLeft, r.left);
          minTop = Math.min(minTop, r.top);
        }
      });
      return {
        dlg_overflow: cs.overflow,
        dlg_pos: cs.position,
        sc_overscroll: scs.overscrollBehavior,
        sc_sh: sc.scrollHeight, sc_ch: sc.clientHeight,
        overflow: {
          right: Math.round(maxRight - dlgRect.right),
          bottom: Math.round(maxBottom - dlgRect.bottom),
          left: Math.round(minLeft - dlgRect.left),
          top: Math.round(minTop - dlgRect.top),
        }
      };
    }""")
    print(f"[{tag}] dialog overflow={info['dlg_overflow']} pos={info['dlg_pos']} | scroller overscroll={info['sc_overscroll']} | sh/ch={info['sc_sh']}/{info['sc_ch']} | 子元素越界(px):", info['overflow'])

    client = ctx.new_cdp_session(page)
    def swipe(x, y1, y2):
        client.send("Input.dispatchTouchEvent", {"type": "touchStart", "touchPoints": [{"x": x, "y": y1}]})
        for i in range(1, 21):
            client.send("Input.dispatchTouchEvent", {"type": "touchMove", "touchPoints": [{"x": x, "y": y1 + (y2 - y1) * i / 20}]})
        client.send("Input.dispatchTouchEvent", {"type": "touchEnd", "touchPoints": []})
    if width < 500:
        page.evaluate("() => { const e = document.querySelector('.overflow-y-auto.pt-3'); if (e) e.scrollTop = 0; }")
        page.wait_for_timeout(400)
        swipe(195, 500, 150)
        page.wait_for_timeout(800)
        st = page.evaluate("() => { const e = document.querySelector('.overflow-y-auto.pt-3'); return e ? e.scrollTop : null; }")
        print(f"[{tag}] 触摸上滑后 scrollTop:", st)
        page.screenshot(path=f"/tmp/mob666_{tag}.png")
    b.close()

with sync_playwright() as p:
    run(p, 390, 844, "mobile")
    run(p, 1280, 800, "desktop")
