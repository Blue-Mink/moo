# 诊断：推荐卡(hero) vs 现 12 组(aurora) 的「柔和度」结构差异
# 推荐卡方法学特征（从 hero-1/2/3 反推）：
#  ① 相邻色相小步走：每步 Δh ≤ ~60°，全程单调（不折返）
#  ② 三停点同亮度带：HSL 亮度 L 差小（hero 全在 45-58%）→ RGB 内插全程高饱和
#  ③ 中间停点按色相行程比例摆放（45-55%）→ 视觉节奏均匀
import itertools

def hsl(hexs):
    h = hexs.lstrip('#'); r,g,b = (int(h[i:i+2],16)/255 for i in (0,2,4))
    mx,mn = max(r,g,b), min(r,g,b); l=(mx+mn)/2
    if mx==mn: return 0.0,0.0,l
    d=mx-mn; s=d/(2-mx-mn) if l>0.5 else d/(mx+mn)
    if mx==r: x=((g-b)/d)%6
    elif mx==g: x=(b-r)/d+2
    else: x=(r-g)/d+4
    return x*60, s, l

def dh(a,b):
    d=abs(a-b)%360; return min(d,360-d)

def report(tag, P):
    print(f"── {tag}")
    print(f"{'组':<4}{'色相轨迹(步Δ)':<34}{'单调':<5}{'亮度L':<22}{'L差':<5}{'最大步'}")
    for i,(n,(a,m,b)) in P.items():
        hs=[hsl(c) for c in (a,m,b)]
        s1,s2 = hs[0][0],hs[1][0]; s3=hs[2][0]
        st1 = dh(s1,s2); st2 = dh(s2,s3); total = dh(s1,s3)
        mono = "是" if st1+st2 <= total+8 else "折返"
        Ls=[h[2]*100 for h in hs]
        print(f"{i:<4}{n:<6}{s1:>5.0f}→{s2:.0f}→{s3:.0f} (Δ{st1:.0f}/{st2:.0f})   {mono:<5}"
              f"{Ls[0]:.0f}/{Ls[1]:.0f}/{Ls[2]:.0f}%        {max(Ls)-min(Ls):<5.0f}{max(st1,st2):.0f}°")

HERO = {
 1:("蓝紫粉",("#2563eb","#7c3aed","#db2777")),
 2:("青碧蓝",("#0d9488","#0ea5e9","#3b82f6")),
 3:("橙红",  ("#f59e0b","#f97316","#ef4444")),
}
CUR = {
 4:("紫红玫",("#c026d3","#e11d48","#fb7185")),
 5:("玫粉薰衣",("#f472b6","#c084fc","#8b5cf6")),
 6:("黄绿松绿",("#84cc16","#22c55e","#15803d")),
 7:("绿暗橙",("#16a34a","#65a30d","#c2410c")),
 8:("靛孔雀",("#4f46e5","#0ea5e9","#059669")),
 9:("午夜紫",("#3730a3","#5b21b6","#8b5cf6")),
 10:("天柠",  ("#0ea5e9","#38bdf8","#a3e635")),
 11:("粉青绿",("#ec4899","#6d28d9","#14b8a6")),
 12:("柠靛",  ("#eab308","#d97706","#4338ca")),
}
report("推荐卡 hero-1..3（柔和温润的参照物）", HERO)
report("现极光 aurora-4..12（生硬感的来源）", CUR)
