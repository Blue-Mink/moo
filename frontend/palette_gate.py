# 极光 12 组 v3 闸门 —— 以推荐卡 hero-1..3 为黄金标准反推的方法学：
#  ① 每步色相 Δ ≤ 75°（hero1 实测 71° 为上限先例）
#  ② 全程单调（不折返）
#  ③ 每条 sRGB 插值段中点色饱和度 ≥ 50%（“温润不互灰”的直接检验；
#     hero2 亮度跨度 28% 依然温润 = 亮度带宽不是主因，中段不饱和坍塌才是）
#  ④ 组间 max(Δ起点, Δ终点) ≥ 30°（身份可辨）
import itertools, sys

def hsl(h):
    h=h.lstrip('#'); r,g,b=(int(h[i:i+2],16)/255 for i in (0,2,4))
    mx,mn=max(r,g,b),min(r,g,b); l=(mx+mn)/2
    if mx==mn: return 0.0,0.0,l
    d=mx-mn; s=d/(2-mx-mn) if l>0.5 else d/(mx+mn)
    x=((g-b)/d)%6 if mx==r else ((b-r)/d+2 if mx==g else (r-g)/d+4)
    return x*60,s,l
def sat(h): return hsl(h)[1]
def dh(a,b):
    d=abs(a-b)%360; return min(d,360-d)
def hexs(t): return '#%02x%02x%02x'%tuple(round((x*255)) for x in t)
def mid(a,b):
    ha,hb=a.lstrip('#'),b.lstrip('#')
    ta=[int(ha[i:i+2],16) for i in (0,2,4)]; tb=[int(hb[i:i+2],16) for i in (0,2,4)]
    return '#%02x%02x%02x'%tuple((x+y)//2 for x,y in zip(ta,tb))

P={
 1:("蓝紫粉hero1",("#2563eb","#7c3aed","#db2777")),
 2:("青碧蓝hero2",("#0d9488","#0ea5e9","#3b82f6")),
 3:("橙红hero3",  ("#f59e0b","#f97316","#ef4444")),
 4:("珊瑚玫红",   ("#f43f5e","#fb923c","#fbbf24")),
 5:("品红紫",     ("#d946ef","#ec4899","#f43f5e")),
 6:("春绿",       ("#84cc16","#22c55e","#10b981")),
 7:("青翠",       ("#22c55e","#10b981","#14b8a6")),
 8:("蓝紫洋红",   ("#4f46e5","#8b5cf6","#c026d3")),
 9:("午夜蓝紫",   ("#1e3a8a","#3730a3","#5b21b6")),
 10:("玫红绯红",  ("#db2777","#e11d48","#f43f5e")),
 11:("柠黄春绿",  ("#eab308","#a3e635","#84cc16")),
 12:("湛蓝青",    ("#3b82f6","#0ea5e9","#06b6d4")),
}
STEP_MAX, SAT_MIN, SEP_MIN = 75.0, 0.50, 30.0
bad=0; pts={}
for i,(n,(a,m,b)) in P.items():
    hs=[hsl(c) for c in (a,m,b)]
    hues=[x[0] for x in hs]
    def fwd(p,q):
        d=(q-p)%360
        return d if d<=180 else d-360
    u=[hues[0], hues[0]+fwd(hues[0],hues[1]), (hues[0]+fwd(hues[0],hues[1]))+fwd(hues[1],hues[2])]
    st=[abs(u[1]-u[0]),abs(u[2]-u[1])]
    mono=(u[0]<=u[1]<=u[2]) or (u[0]>=u[1]>=u[2])
    mids=[sat(mid(a,m)), sat(mid(m,b))]
    tag=f"#{i}{n}"
    if max(st)>STEP_MAX: bad+=1; print(f"FAIL {tag} 步Δ{st[0]:.0f}/{st[1]:.0f}>{STEP_MAX:.0f}")
    if not mono:        bad+=1; print(f"FAIL {tag} 非单调 {['%.0f'%x for x in hues]}")
    if min(mids)<SAT_MIN: bad+=1; print(f"FAIL {tag} 中段饱和坍塌 {mids[0]:.0%}/{mids[1]:.0%} <{SAT_MIN:.0%}")
    pts[i]=(n,hues[0],hues[2])
for (i1,(n1,s1,e1)),(i2,(n2,s2,e2)) in itertools.combinations(pts.items(),2):
    ds,de=dh(s1,s2),dh(e1,e2)
    if max(ds,de)<SEP_MIN:
        bad+=1; print(f"FAIL #{i1}{n1}↔#{i2}{n2} 起Δ{ds:.0f} 终Δ{de:.0f} <{SEP_MIN:.0f}")
print(f"── 12 组 / {len(P)*(len(P)-1)//2} 对：违例 {bad}")
sys.exit(1 if bad else 0)
