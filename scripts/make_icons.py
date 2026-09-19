#!/usr/bin/env python3
"""生成 voice-input 全套图标(可复现,依赖 Pillow):
风格:纯黑白·线性描边·极简(无渐变彩蛋、无声波,单一麦克风线稿)。
  - packaging/AppIcon.icns                  应用图标(默认黑底白线;VARIANT=light 白底黑线)
  - cmd/voice-input/assets/menu.icns        菜单栏模板图标(黑色剪影,适配深浅色)
  - build/preview/                          双色板预览 PNG + HTML
用法: python3 scripts/make_icons.py [ VARIANT=dark|light ]
"""
import os
import subprocess
import shutil
from pathlib import Path

from PIL import Image, ImageDraw

ROOT = Path(__file__).resolve().parent.parent
S = 4  # 超采样倍数(抗锯齿)

VARIANT = os.environ.get("VARIANT", "dark")  # dark=黑底白线 light=白底黑线


def vgrad(size, top, bottom):
    w, h = size
    col = Image.new("RGB", (1, 2))
    col.putpixel((0, 0), top)
    col.putpixel((0, 1), bottom)
    return col.resize((w, h), Image.BICUBIC)


def draw_mic_lines(draw, cx, cy, k, color, stroke):
    """线性麦克风:空心胶囊 + 托架弧 + 立柱 + 底座(统一描边,无填充,无声波)。"""
    w = int(stroke * k)
    r_cap = 76 * k

    # 空心胶囊(麦克风头)
    draw.rounded_rectangle(
        [(cx - 76) * k, (cy - 228) * k, (cx + 76) * k, (cy + 68) * k],
        radius=r_cap, outline=color, width=w)

    # 托架弧(下半圆)
    r = 196 * k
    draw.arc([(cx * k - r, cy * k - r), (cx * k + r, cy * k + r)],
             start=8, end=172, fill=color, width=w)

    def line(x1, y1, x2, y2):
        draw.line([(x1 * k, y1 * k), (x2 * k, y2 * k)], fill=color, width=w)
        rr = w / 2
        for x, y in ((x1, y1), (x2, y2)):
            draw.ellipse([(x * k - rr, y * k - rr), (x * k + rr, y * k + rr)], fill=color)

    # 立柱 + 底座
    line(cx, cy + 196, cx, cy + 300)
    line(cx - 116, cy + 300, cx + 116, cy + 300)


def _hline(draw, k, color, w, x1, x2, y):
    draw.line([(x1 * k, y * k), (x2 * k, y * k)], fill=color, width=int(w * k))
    rr = int(w * k) / 2
    for x in (x1, x2):
        draw.ellipse([(x * k - rr, y * k - rr), (x * k + rr, y * k + rr)], fill=color)


def _capsule_mic(draw, k, color, stroke, cx, top, r_cradle):
    """实心胶囊(麦克风头,经典画法——空心会像温度计)+ 托架弧。"""
    w = int(stroke * k)
    draw.rounded_rectangle(
        [(cx - 76) * k, top * k, (cx + 76) * k, (top + 296) * k],
        radius=76 * k, fill=color)
    cyc = top + 296 - 76  # 托架圆心 = 胶囊底部圆心
    r = r_cradle * k
    draw.arc([(cx * k - r, cyc * k - r), (cx * k + r, cyc * k + r)],
             start=8, end=172, fill=color, width=w)


def draw_concept_a(draw, k, color, stroke):
    """方案A 竖向叙事:麦克风口述 → 下方三行文字 + 光标。"""
    _capsule_mic(draw, k, color, stroke, cx=512, top=200, r_cradle=150)
    lw = stroke  # 文字线与麦克风同粗,视觉统一
    _hline(draw, k, color, lw, 332, 692, 668)   # 第一行(长)
    _hline(draw, k, color, lw, 332, 592, 748)   # 第二行(中)
    _hline(draw, k, color, lw, 332, 500, 828)   # 第三行(短)
    # 文字光标(实心圆角竖条)
    draw.rounded_rectangle([(528) * k, (800) * k, (568) * k, (856) * k],
                           radius=12 * k, fill=color)


def draw_concept_b(draw, k, color, stroke):
    """方案B 横向叙事:左麦克风,右三行文字 + 光标(声音流向文档)。"""
    _capsule_mic(draw, k, color, stroke, cx=300, top=300, r_cradle=140)
    lw = stroke
    _hline(draw, k, color, lw, 540, 812, 356)
    _hline(draw, k, color, lw, 540, 760, 486)
    _hline(draw, k, color, lw, 540, 648, 616)
    draw.rounded_rectangle([(676) * k, (588) * k, (716) * k, (644) * k],
                           radius=12 * k, fill=color)


def make_app_icon(variant):
    """variant: a=竖向叙事 b=横向叙事(定稿)。
    配色:白底黑线(用户定稿 2026-09-19)。"""
    big = 1024 * S
    bg_top, bg_bot = (252, 252, 253), (242, 242, 245)  # 近白
    glyph = (24, 24, 27, 255)  # 近黑

    img = Image.new("RGBA", (big, big), (0, 0, 0, 0))
    bg = vgrad((big, big), bg_top, bg_bot).convert("RGBA")
    mask = Image.new("L", (big, big), 0)
    ImageDraw.Draw(mask).rounded_rectangle([0, 0, big, big], radius=int(230 * S), fill=255)
    img.paste(bg, (0, 0), mask)

    draw = ImageDraw.Draw(img, "RGBA")
    if variant == "b":
        draw_concept_b(draw, S, glyph, stroke=40)
    else:
        draw_concept_a(draw, S, glyph, stroke=40)
    return img.resize((1024, 1024), Image.LANCZOS)


def make_menu_icon():
    """菜单栏模板图标:与应用图标(方案B)同构——左麦克风 + 右三行文字 + 光标。
    纯黑 + alpha(template 自动适配深浅);描边加粗保证 16pt 下可辨;
    输出宽幅 PNG(NSImage 直接渲染,保持横向比例)。"""
    big = 1024
    img = Image.new("RGBA", (big, big), (0, 0, 0, 0))
    draw = ImageDraw.Draw(img, "RGBA")
    black = (0, 0, 0, 255)
    draw_concept_b(draw, 1.0, black, stroke=64)
    # 按内容紧裁,留 6% 边距
    bbox = img.getbbox()
    if bbox:
        pad_x = int((bbox[2] - bbox[0]) * 0.06)
        pad_y = int((bbox[3] - bbox[1]) * 0.06)
        bbox = (max(0, bbox[0] - pad_x), max(0, bbox[1] - pad_y),
                min(big, bbox[2] + pad_x), min(big, bbox[3] + pad_y))
        img = img.crop(bbox)
    # 统一放大到约 2x 渲染精度(高度 ~720px)
    target_h = 720
    scale = target_h / img.height
    img = img.resize((int(img.width * scale), target_h), Image.LANCZOS)
    return img


def build_iconset(master_png: Path, out_icns: Path, sizes=(16, 32, 64, 128, 256, 512)):
    iconset = out_icns.parent / (out_icns.stem + ".iconset")
    if iconset.exists():
        shutil.rmtree(iconset)
    iconset.mkdir(parents=True)
    master = Image.open(master_png)
    for s in sizes:
        for suffix, real in (("", s), ("@2x", s * 2)):
            if real > 1024:
                continue
            master.resize((real, real), Image.LANCZOS).save(iconset / f"icon_{s}x{s}{suffix}.png")
    subprocess.run(["iconutil", "-c", "icns", str(iconset), "-o", str(out_icns)], check=True)
    shutil.rmtree(iconset)


def write_preview(apps: dict, menu: Image.Image):
    prev = ROOT / "build" / "preview"
    prev.mkdir(parents=True, exist_ok=True)
    for name, img in apps.items():
        img.resize((512, 512), Image.LANCZOS).save(prev / f"app_icon_{name}.png")
    bar_light = Image.new("RGBA", (480, 44), (245, 245, 247, 255))
    bar_dark = Image.new("RGBA", (480, 44), (28, 28, 30, 255))
    mh = 26  # 模拟菜单栏 13pt(2x)
    m = menu.resize((int(menu.width * mh / menu.height), mh), Image.LANCZOS)
    bar_light.paste(m, (16, (44 - mh) // 2), m)
    bar_dark.paste(m, (16, (44 - mh) // 2), m)
    bar_light.save(prev / "menubar_light.png")
    bar_dark.save(prev / "menubar_dark.png")


def main():
    a = make_app_icon("a")
    b = make_app_icon("b")
    menu = make_menu_icon()
    write_preview({"a": a, "b": b}, menu)

    chosen = b if VARIANT in ("b",) else a
    tmp = ROOT / "packaging" / "_appicon_1024.png"
    tmp.parent.mkdir(exist_ok=True)
    chosen.save(tmp)
    build_iconset(tmp, ROOT / "packaging" / "AppIcon.icns")

    # 菜单栏图标:宽幅模板 PNG(NSImage 直接渲染)
    menu_path = ROOT / "cmd" / "voice-input" / "assets" / "menu.png"
    menu_path.parent.mkdir(parents=True, exist_ok=True)
    menu.save(menu_path)
    old_icns = ROOT / "cmd" / "voice-input" / "assets" / "menu.icns"
    if old_icns.exists():
        old_icns.unlink()
    tmp.unlink()
    print(f"✅ 图标生成(VARIANT={VARIANT}):packaging/AppIcon.icns + assets/menu.png;双概念预览在 build/preview/")


if __name__ == "__main__":
    main()
