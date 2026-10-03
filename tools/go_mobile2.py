import os
from playwright.sync_api import sync_playwright

BASE = os.environ.get("MOO_BASE", "https://127.0.0.1:5667/app/moo/")  # 脱敏：默认回环，export MOO_BASE 注入实际入口
with sync_playwright() as p:
    b = p.chromium.launch(headless=True, args=["--ignore-certificate-errors"])
    ctx = b.new_context(viewport={"width":390,"height":844}, ignore_https_errors=True, storage_state="/tmp/fnos_state.json")
    page = ctx.new_page()
    page.goto(BASE + "#/apps", wait_until="domcontentloaded", timeout=30000)
    page.wait_for_timeout(6000)
    n = page.locator("input[placeholder='搜索应用...']").count()
    filled = False
    for i in range(n):
        el = page.locator("input[placeholder='搜索应用...']").nth(i)
        if el.is_visible():
            el.click()
            el.fill("genoffice")
            filled = True
            print(f"filled box #{i}")
            break
    print("filled:", filled)
    page.wait_for_timeout(2500)
    # 找可见的 GenOffice 卡（移动端行视图）
    card = page.get_by_text("GenOffice", exact=False).first
    print("card visible:", card.is_visible())
    card.click()
    page.wait_for_selector("[role=dialog]", timeout=10000)
    page.wait_for_timeout(6000)
    page.evaluate("""() => {
      const sc = [...document.querySelectorAll('.overflow-y-auto')].find(e => e.scrollHeight > e.clientHeight + 50);
      const h = sc && [...sc.querySelectorAll('*')].find(e => e.children.length === 0 && e.textContent.trim() === 'README');
      if (h) h.scrollIntoView({block:'start'});
    }""")
    page.wait_for_timeout(5000)
    page.evaluate("""() => {
      const sc = [...document.querySelectorAll('.overflow-y-auto')].find(e => e.scrollHeight > e.clientHeight + 50);
      if (sc) sc.scrollTop += 400;
    }""")
    page.wait_for_timeout(4000)
    info = page.evaluate("""() => {
      const sc = [...document.querySelectorAll('.overflow-y-auto')].find(e => e.scrollHeight > e.clientHeight + 50);
      const failed = [...document.querySelectorAll('img')].filter(im => im.dataset.imgFailed === '1')
        .map(im => ({alt: im.alt, mw: im.style.maxWidth}));
      return {h_overflow: sc ? sc.scrollWidth > sc.clientWidth + 2 : null,
              failed_imgs: failed, readme_fail: document.body.innerText.includes('加载失败')};
    }""")
    print("横向溢出:", info["h_overflow"], "| 破图占位数:", len(info["failed_imgs"]), info["failed_imgs"][:3], "| README失败:", info["readme_fail"])
    page.screenshot(path="/tmp/go_mobile.png")
    b.close()
