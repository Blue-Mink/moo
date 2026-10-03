import os
from playwright.sync_api import sync_playwright

# 入口经 MOO_BASE 注入（脱敏，默认回环）：如 export MOO_BASE="https://<NAS_IP>:5667/app/moo/"
BASE = os.environ.get("MOO_BASE", "https://127.0.0.1:5667/app/moo/")

with sync_playwright() as p:
    b = p.chromium.launch(headless=True, args=["--ignore-certificate-errors"])
    ctx = b.new_context(viewport={"width":390,"height":844}, ignore_https_errors=True, storage_state="/tmp/fnos_state.json")
    page = ctx.new_page()
    page.goto(BASE + "#/apps", wait_until="domcontentloaded", timeout=30000)
    page.wait_for_timeout(6000)
    # 简单稳健：遍历所有匹配，找可见的（移动端/桌面端双搜索框）
    n = page.locator("input[placeholder='搜索应用...']").count()
    for i in range(n):
        el = page.locator("input[placeholder='搜索应用...']").nth(i)
        if el.is_visible():
            el.fill("genoffice")
            break
    page.wait_for_timeout(2000)
    page.locator("h3").first.click()
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
    page.wait_for_timeout(3000)
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
