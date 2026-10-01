#!/usr/bin/env python3
"""0.6.118 双项实测（390x844 移动端视口）：
A) 顶部通知栏：安装进行中不显示进度条通知，完成后提示「XX 安装成功」
B) 安装弹窗（PanelInstallDialog）宽度 = 详情页卡片宽度（390-24=366px）
"""
import json, os, subprocess, sys, time
from playwright.sync_api import sync_playwright

BASE = os.environ.get("MOO_BASE", "http://127.0.0.1:13810/")  # 脱敏：默认回环，export MOO_BASE 注入实际入口
SSH = ["sshpass", "-p", "<SSH_PASSWORD>", "ssh", "-o", "StrictHostKeyChecking=no", "root@" + os.environ.get("NAS_HOST", "127.0.0.1")]

def ssh(cmd):
    r = subprocess.run(SSH + [cmd], capture_output=True, text=True, timeout=90)
    return r.stdout.strip()

def tasks():
    out = ssh("curl -s -m 6 http://127.0.0.1:13812/app/moo/api/tasks")
    try:
        return json.loads(out)
    except Exception:
        return []

def indicator_state(page):
    """返回顶部通知栏状态：None 或文本"""
    try:
        el = page.locator("div.fixed.top-14")
        if el.count() == 0:
            return None
        txt = el.inner_text(timeout=1500)
        return txt.replace("\n", " | ").strip() if txt else "(空)"
    except Exception:
        return "(定位失败)"

result = {"toast_seen": [], "during_install": [], "dialog_width": None, "card_width": None}

with sync_playwright() as p:
    browser = p.chromium.launch(headless=True)
    ctx = browser.new_context(viewport={"width": 390, "height": 844}, is_mobile=True, has_touch=True,
                              user_agent="Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15")
    page = ctx.new_page()
    page.goto(BASE, wait_until="domcontentloaded", timeout=60000)
    page.wait_for_timeout(9000)  # 等目录/列表渲染稳定

    # ---- Phase A: 发起 SBTI 安装（管理通道），观察顶部通知 ----
    key = "SBTI%40shuangji66%E7%9A%84%E5%BA%94%E7%94%A8%E6%BA%90"
    # 后台启动安装（SSE 流写到文件），不阻塞
    subprocess.Popen(
        SSH + [f"nohup curl -s -m 900 -N -X POST 'http://127.0.0.1:13812/app/moo/api/apps/{key}/install' -o /tmp/moo118/sse_install.log > /dev/null 2>&1 & echo started"],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    time.sleep(3)

    t0 = time.time()
    done_notified = False
    while time.time() - t0 < 150:
        st = indicator_state(page)
        tk = tasks()
        run = [t for t in tk if t.get("status") not in ("done", "failed")]
        now_tag = f"{time.time()-t0:4.0f}s ind={st!r} running={len(run)}"
        result["during_install"].append(now_tag)
        if st and "安装成功" in st:
            result["toast_seen"].append({"t": round(time.time() - t0, 1), "text": st, "stage": "done"})
            page.screenshot(path="/tmp/moo118/toast_success.png")
            done_notified = True
            break
        if st and "失败" in st:
            result["toast_seen"].append({"t": round(time.time() - t0, 1), "text": st, "stage": "failed"})
            page.screenshot(path="/tmp/moo118/toast_failed.png")
            done_notified = True
            break
        if st and not done_notified and len(result["during_install"]) == 4:
            # 安装进行中的某一刻截图（确认顶部无进度条）
            page.screenshot(path="/tmp/moo118/during_install.png")
        # 任务已终态但 4s 窗口已过仍未捕获 → 结束
        if run == [] and tk:
            last = tk[-1]
            if last.get("status") in ("done", "failed") and time.time() - t0 > 8:
                result.setdefault("final_task", last)
                break
        time.sleep(1.2)

    # 若 4s 窗口错过（任务完成早于观察循环捕获），补查任务终态
    if not done_notified:
        tk = tasks()
        result.setdefault("final_task", tk[-1] if tk else None)
        page.screenshot(path="/tmp/moo118/after_install.png")

    # ---- Phase B: 官方应用安装弹窗宽度 vs 详情卡片宽度 ----
    # 回到列表（当前就在列表），搜索 AdGuardHome
    try:
        page.get_by_placeholder("搜索").click()
        page.get_by_placeholder("搜索").fill("AdGuardHome")
        page.wait_for_timeout(2500)
        row = page.locator("text=AdGuardHome").first
        row.click()
        page.wait_for_timeout(2500)
        # 详情卡片：全屏对话框（.fixed.inset-0）
        card = page.locator("[role=dialog].fixed.inset-0").last
        # 卡片内层容器 px-3 → 量它的 paddingBox 内宽
        card_w = card.bounding_box()
        # 点头部安装按钮 → PanelInstallDialog
        page.locator("[role=dialog].fixed.inset-0 button:has-text('安装')").last.click()
        page.wait_for_timeout(3500)
        dlg = page.locator("[role=dialog]").last
        dlg_w = dlg.bounding_box()
        # 详情页卡片实际可见宽度 = 对话框宽 - 2*12px(px-3)
        result["card_width"] = round(card_w["width"] - 24, 1) if card_w else None
        result["dialog_width"] = round(dlg_w["width"], 1) if dlg_w else None
        result["dialog_box"] = dlg_w
        page.screenshot(path="/tmp/moo118/dialog_width.png")
    except Exception as e:
        result["phaseB_error"] = str(e)[:300]

    browser.close()

print(json.dumps(result, ensure_ascii=False, indent=1))
