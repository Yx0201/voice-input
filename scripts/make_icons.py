#!/usr/bin/env python3
"""生成 voice-input 全套图标 v2(波形设计,源图驱动,可复现;依赖 Pillow)。

源图(用户提供,入库保证可复现):
  cmd/voice-input/assets/icon-source-app.png   宽幅白条波形(黑底,272x88)
  cmd/voice-input/assets/icon-source-menu.png  紧凑 9 条波形(黑底,80x66)

做法:解析源图中每根波形条的精确几何,在高分辨率上**矢量重绘**(圆头矩形),
避免直接放大位图发糊。

产物:
  packaging/AppIcon.icns                     应用图标(黑底白波形圆角方块)
  build/preview/app_icon.png                 应用图标预览
  build/preview/menu_template.png            菜单模板图标预览(黑+透明)
  build/preview/menubar_light|dark.png       菜单栏深浅色模拟
  build/preview/menu_compare.png             新旧菜单图标对比

⚠️ 菜单模板图标默认**不落盘**到 assets/menu.png——先出预览给用户确认,
确认后 `APPLY_MENU=1 python3 scripts/make_icons.py` 才正式启用。

用法:
  python3 scripts/make_icons.py                # 应用图标 + 全部预览(不动菜单图标)
  APPLY_MENU=1 python3 scripts/make_icons.py   # 预览确认后正式启用菜单图标
"""
import os
import shutil
import subprocess
from pathlib import Path

from PIL import Image, ImageDraw

ROOT = Path(__file__).resolve().parent.parent
ASSETS = ROOT / "cmd" / "voice-input" / "assets"
SS = 4  # 超采样倍数(抗锯齿)

APPLY_MENU = os.environ.get("APPLY_MENU") == "1"


def parse_bars(img: Image.Image, thresh: int = 100):
    """解析黑底白条波形:返回每根条的 (x0, x1, top, bottom)(含端点)。"""
    g = img.convert("L")
    w, h = g.size
    px = g.load()
    cols = []
    for x in range(w):
        top = bottom = None
        for y in range(h):
            if px[x, y] > thresh:
                if top is None:
                    top = y
                bottom = y
        cols.append((top, bottom))
    bars, x = [], 0
    while x < w:
        if cols[x][0] is not None:
            x2 = x
            while x2 + 1 < w and cols[x2 + 1][0] is not None:
                x2 += 1
            tops = [cols[i][0] for i in range(x, x2 + 1)]
            bots = [cols[i][1] for i in range(x, x2 + 1)]
            bars.append((x, x2, min(tops), max(bots)))
            x = x2 + 1
        else:
            x += 1
    return bars


def draw_bars(draw, bars, scale, ox, oy, fill):
    """按解析几何重绘波形条:每根为圆头矩形(半径=宽/2,胶囊端)。"""
    for x0, x1, top, bot in bars:
        x = ox + x0 * scale
        y = oy + top * scale
        w = max((x1 - x0 + 1) * scale, 1)
        h = max((bot - top + 1) * scale, 1)
        draw.rounded_rectangle([x, y, x + w, y + h], radius=min(w, h) / 2, fill=fill)


def make_app_icon() -> Image.Image:
    """应用图标:1024 圆角黑方块 + 居中白波形(波形宽约 72%)。"""
    src = Image.open(ASSETS / "icon-source-app.png")
    bars = parse_bars(src)
    size = 1024
    canvas = Image.new("RGBA", (size * SS, size * SS), (0, 0, 0, 0))
    d = ImageDraw.Draw(canvas)
    # macOS 圆角 ≈ 边长 22.5%(超采样坐标系)
    d.rounded_rectangle([0, 0, size * SS - 1, size * SS - 1],
                        radius=int(size * SS * 0.225), fill=(10, 10, 10, 255))
    glyph_w = 0.72 * size * SS
    scale = glyph_w / src.width
    gw = src.width * scale
    gh = src.height * scale
    draw_bars(d, bars, scale, (size * SS - gw) / 2, (size * SS - gh) / 2,
              (255, 255, 255, 255))
    return canvas.resize((size, size), Image.LANCZOS)


def make_menu_template() -> Image.Image:
    """菜单栏模板图标:纯黑 + 透明底(高分辨率,NSImage 模板渲染自动适配深浅色)。"""
    src = Image.open(ASSETS / "icon-source-menu.png")
    bars = parse_bars(src)
    target_h = 176  # 约 4x 点尺寸,余量充足
    scale = target_h * SS / src.height
    w = int(src.width * scale) + 8 * SS
    h = target_h * SS + 8 * SS
    canvas = Image.new("RGBA", (w, h), (0, 0, 0, 0))
    d = ImageDraw.Draw(canvas)
    draw_bars(d, bars, scale, 4 * SS, 4 * SS, (0, 0, 0, 255))
    return canvas.resize((w // SS, h // SS), Image.LANCZOS)


def build_iconset(master: Image.Image, out_icns: Path, sizes=(16, 32, 64, 128, 256, 512)):
    iconset = out_icns.parent / (out_icns.stem + ".iconset")
    if iconset.exists():
        shutil.rmtree(iconset)
    iconset.mkdir(parents=True)
    for s in sizes:
        for suffix, real in (("", s), ("@2x", s * 2)):
            master.resize((real, real), Image.LANCZOS).save(
                iconset / f"icon_{s}x{s}{suffix}.png")
    subprocess.run(["iconutil", "-c", "icns", str(iconset), "-o", str(out_icns)],
                   check=True)
    shutil.rmtree(iconset)


def write_previews(app: Image.Image, menu: Image.Image):
    prev = ROOT / "build" / "preview"
    prev.mkdir(parents=True, exist_ok=True)
    app.save(prev / "app_icon.png")
    menu.save(prev / "menu_template.png")

    # 菜单栏深浅色模拟(条高 44@2x,图标按 13pt=26px 竖直居中)
    strips = []
    for name, bg in (("light", (245, 245, 247, 255)), ("dark", (28, 28, 30, 255))):
        bar = Image.new("RGBA", (480, 44), bg)
        mh = 26
        m = menu.resize((int(menu.width * mh / menu.height), mh), Image.LANCZOS)
        bar.paste(m, (16, (44 - mh) // 2), m)
        bar.save(prev / f"menubar_{name}.png")
        strips.append(bar)

    # 新旧对比(有旧图标时)
    old_path = ASSETS / "menu.png"
    if old_path.exists():
        old = Image.open(old_path).convert("RGBA")
        comp = Image.new("RGBA", (480, 120), (255, 255, 255, 255))
        for i, (icon, bar) in enumerate(((old, strips[0]), (menu, strips[1]))):
            y = i * 60
            comp.paste(bar, (0, y))
            mh = 26
            m = icon.resize((int(icon.width * mh / icon.height), mh), Image.LANCZOS)
            comp.paste(m, (360, y + (44 - mh) // 2), m)
        comp.save(prev / "menu_compare.png")


def main():
    app = make_app_icon()
    menu = make_menu_template()

    build_iconset(app, ROOT / "packaging" / "AppIcon.icns")
    write_previews(app, menu)

    if APPLY_MENU:
        menu.save(ASSETS / "menu.png")
        print("✅ 应用图标已生成;菜单模板图标已正式启用(assets/menu.png)")
    else:
        print("✅ 应用图标已生成;菜单模板图标仅输出预览(build/preview/),"
              "确认后 APPLY_MENU=1 重新运行即正式启用")


if __name__ == "__main__":
    main()
