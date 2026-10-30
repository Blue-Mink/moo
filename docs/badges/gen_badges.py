#!/usr/bin/env python3
"""生成 README 顶部徽章的本地静态图（替代 img.shields.io 直连）。

背景（2026-10-29）：Moo 详情页 README 的 4 个 shields.io 徽章在国内网络
（实测本机出口 10s 超时 http=000）加载不出来；Moo 前端只代理 github 系
图片，shields 不代理。改成仓库内静态图后，图片随 README 走 readme-img
代理，不再依赖第三方徽章服务。

用法：python3 docs/badges/gen_badges.py 0.6.374   # 版本号变了重跑一次
"""
import sys, os
from PIL import Image, ImageDraw, ImageFont

OUT = os.path.dirname(os.path.abspath(__file__))
CJK = "/usr/share/fonts/truetype/droid/DroidSansFallbackFull.ttf"
LAT = "/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf"
H, PAD, FS, SCALE = 20, 6, 11, 2          # 显示高 20px，按 2 倍出图
LABEL_BG = (85, 85, 85)
FG = (255, 255, 255)


def font_for(text):
    """含非 ASCII 时用 CJK 字体，否则用 DejaVuSans-Bold。"""
    path = CJK if any(ord(c) > 127 for c in text) else LAT
    return ImageFont.truetype(path, FS * SCALE)


def width(text, font):
    box = font.getbbox(text)
    return box[2] - box[0]


def badge(label, value, color, path):
    h = H * SCALE
    fl, fv = font_for(label), font_for(value)
    wl = width(label, fl) + PAD * SCALE * 2
    wv = width(value, fv) + PAD * SCALE * 2
    img = Image.new("RGB", (wl + wv, h), LABEL_BG)
    d = ImageDraw.Draw(img)
    d.rectangle([wl, 0, wl + wv, h], fill=color)
    # 基线对齐：DroidSansFallback 与 DejaVu 的行高不同，分别居中
    for text, f, x0 in ((label, fl, 0), (value, fv, wl)):
        bx = f.getbbox(text)
        d.text((x0 + PAD * SCALE, (h - (bx[3] - bx[1])) // 2 - bx[1]), text, font=f, fill=FG)
    img.save(path, optimize=True)
    print(f"{os.path.basename(path)}  {img.size[0]}x{img.size[1]}  {os.path.getsize(path)}B")


def main():
    ver = sys.argv[1] if len(sys.argv) > 1 else "0.0.0"
    badge("FPK", ver, (0x1F, 0x6F, 0xEB), os.path.join(OUT, "fpk.png"))
    badge("Go", "单二进制", (0x0A, 0x84, 0xFF), os.path.join(OUT, "go.png"))
    badge("fnOS", "x86_64 / arm64", (0x6F, 0x42, 0xC1), os.path.join(OUT, "platform.png"))
    badge("License", "MIT", (0x11, 0x18, 0x27), os.path.join(OUT, "license.png"))


if __name__ == "__main__":
    main()
