#!/usr/bin/env python3
import json, os, subprocess, time
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
res={"toast":None,"samples":[]}
with sync_playwright() as p:
    b=p.chromium.launch(headless=True)
    ctx=b.new_context(viewport={"width":390,"height":844},is_mobile=True,has_touch=True)
    pg=ctx.new_page(); pg.goto(BASE,wait_until="domcontentloaded",timeout=60000); pg.wait_for_timeout(6000)
    subprocess.Popen(SSH+[f"nohup curl -s -m 300 -N -X POST 'http://127.0.0.1:13812/app/moo/api/apps/allinssl%40fnos-official/install' -o /tmp/moo118f/sse.log >/dev/null 2>&1 & echo ok"],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    time.sleep(2); t0=time.time()
    while time.time()-t0<60:
        s=ind(pg); res["samples"].append(f"{time.time()-t0:3.0f}s {s!r}")
        if s and ("成功" in s or "失败" in s):
            res["toast"]={"t":round(time.time()-t0,1),"text":s}
            pg.screenshot(path="/tmp/moo118/toast_final.png"); break
        time.sleep(1)
    res["sse"]=ssh("tail -c 160 /tmp/moo118f/sse.log | tr -d '\\0'")
    b.close()
print(json.dumps(res,ensure_ascii=False,indent=1))
