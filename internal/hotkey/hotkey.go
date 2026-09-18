// Package hotkey 提供全局热键监听(自实现 CGEventTap,macOS 专用)。
// 相比第三方库:事件 tap 与 runloop 全程自管,不与菜单栏应用的
// NSApplication 事件循环互相干扰;需要辅助功能权限。
package hotkey

/*
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation
#include "tap.h"
*/
import "C"

import (
	"fmt"
	"strings"
)

// onPress 每次命中组合键时回调(在独立 goroutine 中执行,不阻塞事件线程)。
var onPress func()

//export htOnPress
func htOnPress() {
	if onPress != nil {
		go onPress()
	}
}

// modifierBits 修饰键名 → CGEventFlags 位。
var modifierBits = map[string]uint64{
	"ctrl":    1 << 18, // kCGEventFlagMaskControl
	"alt":     1 << 19, // kCGEventFlagMaskAlternate(Option)
	"option":  1 << 19,
	"shift":   1 << 17, // kCGEventFlagMaskShift
	"cmd":     1 << 20, // kCGEventFlagMaskCommand
	"command": 1 << 20,
}

// keyCodes ANSI 虚拟键码(macOS 布局,字母并非连续编号)。
var keyCodes = map[rune]int{
	'a': 0x00, 's': 0x01, 'd': 0x02, 'f': 0x03, 'h': 0x04, 'g': 0x05,
	'z': 0x06, 'x': 0x07, 'c': 0x08, 'v': 0x09, 'b': 0x0B, 'q': 0x0C,
	'w': 0x0D, 'e': 0x0E, 'r': 0x0F, 'y': 0x10, 't': 0x11, 'o': 0x1F,
	'u': 0x20, 'i': 0x22, 'p': 0x23, 'l': 0x25, 'j': 0x26, 'k': 0x28,
	'n': 0x2D, 'm': 0x2E,
	'1': 0x12, '2': 0x13, '3': 0x14, '4': 0x15, '5': 0x17,
	'6': 0x16, '7': 0x1A, '8': 0x1C, '9': 0x19, '0': 0x1D,
}

// Listen 阻塞当前 goroutine:命中「指定修饰键(精确匹配)+ 主键」时回调 onPress。
// tap 创建成功后先回调一次 onReady,再进入事件循环。
// 返回 error = tap 创建失败(典型:辅助功能未授权),可稍后重试。
func Listen(mods []string, key string, cb func(), onReady func()) error {
	var mask uint64
	for _, m := range mods {
		bit, ok := modifierBits[strings.ToLower(strings.TrimSpace(m))]
		if !ok {
			return fmt.Errorf("未知修饰键 %q(可选 ctrl/alt/shift/cmd)", m)
		}
		mask |= bit
	}
	runes := []rune(strings.ToLower(strings.TrimSpace(key)))
	if len(runes) != 1 {
		return fmt.Errorf("无效热键主键 %q(单个字母或数字)", key)
	}
	code, ok := keyCodes[runes[0]]
	if !ok {
		return fmt.Errorf("不支持的热键主键 %q", key)
	}

	onPress = cb
	if rc := C.ht_create_tap(C.ulonglong(mask), C.int(code)); rc != 0 {
		return fmt.Errorf("事件监听创建失败(检查 系统设置→隐私与安全性→辅助功能)")
	}
	if onReady != nil {
		onReady()
	}
	C.ht_run_loop() // 阻塞
	return nil
}
