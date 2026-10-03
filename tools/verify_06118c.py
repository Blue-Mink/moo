#!/usr/bin/env python3
"""0.6.118 Phase B 补测：官方应用安装弹窗（PanelInstallDialog）宽度 vs 详情页卡片宽度。
预期：移动端 390px 下两者都 = 366px（卡片=全屏框内 px-3 内卡；弹窗=max-w-[calc(100vw-1.5rem)]）。
"""
import json, os
from playwright.sync_api import sync_playwright

BASE = os.environ.get("MOO_BASE", "http://127.0.0.1:13810/")  # 脱敏：默认回环，export MOO_BASE 注入实际入口
result = {}

with sync_playwright() as p:
    browser = p.chromium.launch(headless=True)
    ctx = browser.new_context(viewport={"width": 390, "height": 844}, is_mobile=True, has_touch=True)
    page = ctx.new_page()
    page.goto(BASE, wait_until="domcontentloaded", timeout=60000)
    page.wait_for_timeout(9000)

    # 列表卡宽度（移动端 AppRowList 容器，参照物）
    try:
        lb = page.locator("div.bg-card.rounded-[18px]").first.bounding_box()
        result["list_card_width"] = round(lb["width"], 1) if lb else None
    except Exception:
        result["list_card_width"] = None

    # 移动端搜索是收起态：点药丸（aria-label=搜索）展开
    page.locator("button[aria-label='搜索']").first.click()
    page.wait_for_timeout(600)
    page.locator("input.w-full[placeholder='搜索应用...']").fill("ALLinSSL")
    page.wait_for_timeout(3000)
    page.locator("span.font-semibold:has-text('ALLinSSL')").first.click()
    page.wait_for_timeout(2500)

    # 详情页全屏对话框
    detail = page.locator("[role=dialog].inset-0").last
    card_box = detail.bounding_box()
    result["detail_box"] = card_box
    # 内卡 = px-3 容器内的圆角卡（取容器内容宽）
    inner = detail.locator("> div").first
    result["inner_box"] = inner.bounding_box()

    # 点「安装」→ PanelInstallDialog
    page.locator("[role=dialog].inset-0 button:has-text('安装')").last.click()
    page.wait_for_timeout(4000)

    dialogs = page.locator("[role=dialog]")
    result["dialog_count"] = dialogs.count()
    dlg = dialogs.last
    dlg_box = dlg.bounding_box()
    result["dlg_box"] = dlg_box
    result["dlg_class"] = dlg.get_attribute("class")
    # 计算：弹窗宽 vs (详情全屏宽 - 24)
    if card_box and dlg_box:
        result["card_visible_width"] = round(card_box["width"] - 24, 1)
        result["dialog_width"] = round(dlg_box["width"], 1)
        result["delta"] = round(dlg_box["width"] - (card_box["width"] - 24), 1)
    page.screenshot(path="/tmp/moo118/dialog_width.png")

    # 弹窗内容确认（papersplit 无 webui 端口时可能有卷位置等字段）
    result["dlg_text"] = dlg.inner_text(timeout=3000)[:200].replace("\n", " | ")
    browser.close()

print(json.dumps(result, ensure_ascii=False, indent=1))
