#!/usr/bin/env python3
"""0.6.118 前端 E2E：顶部通知栏在完成时弹「X 安装成功/失败」（无进度条）。
开 390px 页 → 等基线 → 管理通道触发安装 → 每秒采样顶部指示器 → 命中终态即截图。
"""
import json, os, subprocess, time
from playwright.sync_api import sync_playwright

BASE = os.environ.get("MOO_BASE", "http://127.0.0.1:13810/")  # 脱敏：默认回环，export MOO_BASE 注入实际入口
SSH = ["sshpass", "-p", "<SSH_PASSWORD>", "ssh", "-o", "StrictHostKeyChecking=no", "root@" + os.environ.get("NAS_HOST", "127.0.0.1")]

def ssh(cmd):
    r = subprocess.run(SSH + [cmd], capture_output=True, text=True, timeout=60, errors="replace")
    return r.stdout.strip()

def indicator_state(page):
    try:
        el = page.locator("div.fixed.top-14")
        if el.count() == 0:
            return None
        txt = el.inner_text(timeout=1200)
        return txt.replace("\n", " | ").strip() if txt else "(empty)"
    except Exception:
        return "(err)"

result = {"samples": [], "toast": None}

with sync_playwright() as p:
    browser = p.chromium.launch(headless=True)
    ctx = browser.new_context(viewport={"width": 390, "height": 844}, is_mobile=True, has_touch=True)
    page = ctx.new_page()
    page.goto(BASE, wait_until="domcontentloaded", timeout=60000)
    page.wait_for_timeout(6000)  # 等首屏 + 基线建立
    result["samples"].append(f"baseline ind={indicator_state(page)!r}")

    # 触发安装（管理通道，后台 SSE）
    key = "alist3%40fnos-official"
    subprocess.Popen(
        SSH + [f"nohup curl -s -m 300 -N -X POST 'http://127.0.0.1:13812/app/moo/api/apps/{key}/install' -o /tmp/moo118b/sse5.log >/dev/null 2>&1 & echo ok"],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    time.sleep(2)

    t0 = time.time()
    while time.time() - t0 < 45:
        st = indicator_state(page)
        result["samples"].append(f"{time.time()-t0:3.0f}s {st!r}")
        if st and ("成功" in st or "失败" in st):
            result["toast"] = {"t": round(time.time() - t0, 1), "text": st}
            page.screenshot(path="/tmp/moo118/toast_e2e.png")
            break
        time.sleep(1)

    # SSE 结果兜底
    try:
        result["sse_tail"] = ssh("tail -c 200 /tmp/moo118b/sse5.log | tr -d '\\0'")
    except Exception:
        pass
    browser.close()

print(json.dumps(result, ensure_ascii=False, indent=1))
