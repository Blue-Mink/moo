#!/usr/bin/env python3
"""0.6.118 补测：真实安装（papersplit 官方应用，未安装）的顶部通知行为。
进行中不应出现进度通知；完成后应提示「papersplit 安装成功」（或失败提示）。
"""
import json, os, subprocess, time
from playwright.sync_api import sync_playwright

BASE = os.environ.get("MOO_BASE", "http://127.0.0.1:13810/")  # 脱敏：默认回环，export MOO_BASE 注入实际入口
SSH = ["sshpass", "-p", "<SSH_PASSWORD>", "ssh", "-o", "StrictHostKeyChecking=no", "root@" + os.environ.get("NAS_HOST", "127.0.0.1")]

def ssh(cmd):
    r = subprocess.run(SSH + [cmd], capture_output=True, text=True, timeout=120)
    return r.stdout.strip()

def indicator_state(page):
    try:
        el = page.locator("div.fixed.top-14")
        if el.count() == 0:
            return None
        txt = el.inner_text(timeout=1500)
        return txt.replace("\n", " | ").strip() if txt else "(空)"
    except Exception:
        return "(定位失败)"

result = {"samples": [], "toast": None}

with sync_playwright() as p:
    browser = p.chromium.launch(headless=True)
    ctx = browser.new_context(viewport={"width": 390, "height": 844}, is_mobile=True, has_touch=True)
    page = ctx.new_page()
    page.goto(BASE, wait_until="domcontentloaded", timeout=60000)
    page.wait_for_timeout(9000)

    key = "papersplit%40fnos-official"
    subprocess.Popen(
        SSH + [f"nohup curl -s -m 900 -N -X POST 'http://127.0.0.1:13812/app/moo/api/apps/{key}/install' -o /tmp/moo118/sse_install2.log > /dev/null 2>&1 & echo started"],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    time.sleep(3)

    t0 = time.time()
    while time.time() - t0 < 240:
        st = indicator_state(page)
        result["samples"].append(f"{time.time()-t0:4.0f}s ind={st!r}")
        if st and ("成功" in st or "失败" in st):
            result["toast"] = {"t": round(time.time() - t0, 1), "text": st}
            page.screenshot(path="/tmp/moo118/toast2.png")
            time.sleep(1)
            break
        if len(result["samples"]) == 6:
            page.screenshot(path="/tmp/moo118/during2.png")
        time.sleep(1.2)

    out = ssh("tail -c 300 /tmp/moo118/sse_install2.log")
    result["sse_tail"] = out
    browser.close()

print(json.dumps(result, ensure_ascii=False, indent=1))
