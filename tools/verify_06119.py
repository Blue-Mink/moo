#!/usr/bin/env python3
"""0.6.119 E2E：安装成功时不再弹大弹窗（successInfo 已删），顶部通知栏弹「XX 安装成功」。"""
import json, os, subprocess, time, urllib.parse
from playwright.sync_api import sync_playwright
BASE=os.environ.get("MOO_BASE","http://127.0.0.1:13810/")  # 脱敏：默认回环，export MOO_BASE 注入实际入口
SSH=["sshpass","-p","<SSH_PASSWORD>","ssh","-o","StrictHostKeyChecking=no","root@"+os.environ.get("NAS_HOST","127.0.0.1")]
def ssh(c):
    return subprocess.run(SSH+[c],capture_output=True,text=True,timeout=60,errors="replace").stdout.strip()
def ind(page):
    try:
        e=page.locator("div.fixed.top-14")
        return e.inner_text(timeout=1200).replace("\n"," | ").strip() if e.count() else None
    except Exception: return None
def success_dialog_visible(page):
    # 成功大弹窗特征：含「安装成功」绿字 + 「确定」按钮的 DialogContent
    try:
        return page.locator("[role=dialog] :text('安装成功')").count() > 0
    except Exception:
        return False
res={"toast":None,"dialog_seen":False,"samples":[]}
wizard=[{"key":"port","value":"18443"},{"key":"safety","value":"https"},
        {"key":"username","value":"admin"},{"key":"password","value":"<TEST_PWD>"}]
wz=urllib.parse.quote(json.dumps(wizard,ensure_ascii=False))
key="allinssl%40fnos-official"
with sync_playwright() as p:
    b=p.chromium.launch(headless=True)
    ctx=b.new_context(viewport={"width":390,"height":844},is_mobile=True,has_touch=True)
    pg=ctx.new_page(); pg.goto(BASE,wait_until="domcontentloaded",timeout=60000); pg.wait_for_timeout(6000)
    subprocess.Popen(SSH+[f"nohup curl -s -m 300 -N -X POST 'http://127.0.0.1:13812/app/moo/api/apps/{key}/install?wizard={wz}' -o /tmp/moo119/sse.log >/dev/null 2>&1 & echo ok"],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    time.sleep(2); t0=time.time()
    while time.time()-t0<90:
        s=ind(pg); dlg=success_dialog_visible(pg)
        if dlg: res["dialog_seen"]=True
        res["samples"].append(f"{time.time()-t0:3.0f}s ind={s!r} dlg={dlg}")
        if s and ("成功" in s or "失败" in s):
            res["toast"]={"t":round(time.time()-t0,1),"text":s}
            pg.screenshot(path="/tmp/moo118/no_dialog_success.png"); break
        time.sleep(1)
    res["sse"]=ssh("tail -c 200 /tmp/moo119/sse.log | tr -d '\\0'")
    b.close()
print(json.dumps(res,ensure_ascii=False,indent=1))
