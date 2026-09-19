package main

// 菜单栏图标:与应用图标同构的宽幅模板 PNG(macOS 按深浅色自动着色)。
// 生成方式:python3 scripts/make_icons.py(依赖 Pillow,产物已入库,构建无需 Python)。

import (
	_ "embed"
)

//go:embed assets/menu.png
var menuIcon []byte
