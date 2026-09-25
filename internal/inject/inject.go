// Package inject 把文本"打"到当前聚焦的输入框。
// 注入方式:剪贴板 + Cmd+V(2026-09-25 定稿)。
// 原键盘逐块注入(20字符/事件)会被 Chromium 类输入框静默丢事件
// (实测 56 字丢 7 字),撤销按账本退格便多吃进上一段;粘贴是原子动作,
// 输入框要么全收到要么全没收到,账本与实际文本必然一致。
// 需要 辅助功能 权限;粘贴后还原用户剪贴板(约 150ms 窗口)。
package inject

/*
#cgo LDFLAGS: -framework AppKit -framework CoreGraphics -framework CoreFoundation -framework ApplicationServices
#include <stdlib.h>
#include "inject.h"
*/
import "C"

import (
	"time"
	"unsafe"
)

// pasteSettle 粘贴事件后等待应用取走文本的窗口;之后还原剪贴板。
const pasteSettle = 150 * time.Millisecond

// TypeText 把整段文本打进焦点输入框:备份剪贴板 → 写入 → Cmd+V → 还原。
// 文本中的换行符随粘贴自然落为换行(不是 Return 键,不会触发聊天框发送)。
// 仅由打字执行线(typeLoop)串行调用,剪贴板操作无重入风险。
func TypeText(s string) {
	if s == "" {
		return
	}
	backup := C.clipGet()
	defer C.free(unsafe.Pointer(backup))

	cstr := C.CString(s)
	defer C.free(unsafe.Pointer(cstr))
	C.clipPut(cstr)
	C.postCmdV()
	time.Sleep(pasteSettle)

	if backup != nil {
		C.clipPut(backup)
	} else {
		C.clipClear()
	}
}

// RequestAccessibility 检查辅助功能权限;未授权时触发系统弹窗引导用户去设置。
func RequestAccessibility() bool {
	return C.checkAccessibility(1) == 1
}

// IsAccessibilityGranted 只检查辅助功能权限状态,不弹任何窗。
func IsAccessibilityGranted() bool {
	return C.checkAccessibility(0) == 1
}
