// Package hotkey 提供全局热键监听(自实现 CGEventTap,macOS 专用)。
// 支持两种模式:
//   toggle —— 组合键按下触发一次,事件放行(不吞)
//   ptt    —— 按住说话:按下/抬起分别回调 onKeyDown/onKeyUp,
//             命中事件被吞掉(不会向输入框打出空格/字符)
// Listen 阻塞运行;Stop 可停止以便热重配。均需辅助功能权限。
package hotkey

/*
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation
#include "tap.h"
*/
import "C"

import (
	"fmt"
	"strings"
	"sync"
)

// onPress/onRelease 由 C 回调进入(事件线程),实现只做投递不阻塞。
var (
	onPress    func()
	onRelease  func()
	callbackMu sync.Mutex
)

//export htOnPress
func htOnPress() {
	callbackMu.Lock()
	f := onPress
	callbackMu.Unlock()
	if f != nil {
		go f()
	}
}

//export htOnRelease
func htOnRelease() {
	callbackMu.Lock()
	f := onRelease
	callbackMu.Unlock()
	if f != nil {
		go f()
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

// keyCodes ANSI 虚拟键码(macOS 布局,字母并非连续编号;空格=49)。
var keyCodes = map[rune]int{
	'a': 0x00, 's': 0x01, 'd': 0x02, 'f': 0x03, 'h': 0x04, 'g': 0x05,
	'z': 0x06, 'x': 0x07, 'c': 0x08, 'v': 0x09, 'b': 0x0B, 'q': 0x0C,
	'w': 0x0D, 'e': 0x0E, 'r': 0x0F, 'y': 0x10, 't': 0x11, 'o': 0x1F,
	'u': 0x20, 'i': 0x22, 'p': 0x23, 'l': 0x25, 'j': 0x26, 'k': 0x28,
	'n': 0x2D, 'm': 0x2E,
	'1': 0x12, '2': 0x13, '3': 0x14, '4': 0x15, '5': 0x17,
	'6': 0x16, '7': 0x1A, '8': 0x1C, '9': 0x19, '0': 0x1D,
	' ': 0x31, // kVK_Space
}

// parse 组合键配置 → (修饰键掩码, 键码)。主键支持单字母/数字或 "space"。
func parse(mods []string, key string) (uint64, int, error) {
	var mask uint64
	for _, m := range mods {
		bit, ok := modifierBits[strings.ToLower(strings.TrimSpace(m))]
		if !ok {
			return 0, 0, fmt.Errorf("未知修饰键 %q(可选 ctrl/alt/shift/cmd)", m)
		}
		mask |= bit
	}
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "space" {
		return mask, keyCodes[' '], nil
	}
	runes := []rune(key)
	if len(runes) != 1 {
		return 0, 0, fmt.Errorf(`无效热键主键 %q(单个字母、数字或 "space")`, key)
	}
	code, ok := keyCodes[runes[0]]
	if !ok {
		return 0, 0, fmt.Errorf("不支持的热键主键 %q", key)
	}
	return mask, code, nil
}

// Listen toggle 模式:组合键按下触发 onPress,事件放行。阻塞;返回即出错。
func Listen(mods []string, key string, cb func(), onReady func()) error {
	return ListenPTT(mods, key, cb, nil, onReady)
}

// ListenPTT 按住说话模式:onKeyDown=按下触发,onKeyUp=抬起触发;
// 命中事件被吞掉(不会向任何应用打出字符/空格)。onKeyUp 为 nil 时退化为 toggle。
// 阻塞运行;正常情况永不返回(Stop 可使其返回 nil)。
func ListenPTT(mods []string, key string, onKeyDown, onKeyUp func(), onReady func()) error {
	mask, code, err := parse(mods, key)
	if err != nil {
		return err
	}

	callbackMu.Lock()
	onPress = onKeyDown
	onRelease = onKeyUp
	callbackMu.Unlock()

	consume := 0
	if onKeyUp != nil {
		consume = 1
	}
	if rc := C.ht_create_tap(C.ulonglong(mask), C.int(code), C.int(consume)); rc != 0 {
		return fmt.Errorf("事件监听创建失败(检查 系统设置→隐私与安全性→辅助功能)")
	}
	if onReady != nil {
		onReady()
	}
	C.ht_run_loop() // 阻塞;ht_stop_loop 使其返回
	return nil
}

// Stop 停止当前监听(用于热重配/模式切换),使阻塞中的 ListenPTT 返回。
func Stop() {
	C.ht_stop_loop()
}
