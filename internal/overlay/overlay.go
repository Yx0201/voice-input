// Package overlay 在光标旁显示"AI 转换中"的 loading 悬浮层,
// 告诉用户识别在途、系统没有卡死。引用计数 + 最短显示防闪烁。
package overlay

/*
#cgo LDFLAGS: -framework AppKit -framework ApplicationServices -framework Foundation
#include "spinner.h"
*/
import "C"

import (
	"log"
	"sync"
	"time"
)

const minDisplay = 300 * time.Millisecond // 防止一闪而过

var (
	mu    sync.Mutex
	count int
	hideT *time.Timer
)

// Busy 标记一段识别在途(进入时 spinner 出现;全部结束后延迟隐藏)。
// 可重入:嵌套/并发调用按计数抵消。
func Busy() func() {
	mu.Lock()
	count++
	if count == 1 {
		if hideT != nil {
			hideT.Stop()
			hideT = nil
		}
		show()
	}
	mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			mu.Lock()
			count--
			if count <= 0 {
				count = 0
				hideT = time.AfterFunc(minDisplay, hide)
			}
			mu.Unlock()
		})
	}
}

func show() {
	C.overlay_init()
	var x, y C.double
	caretOK := C.overlay_caret_position(&x, &y) == 1
	if !caretOK {
		C.overlay_fallback_position(&x, &y)
	}
	C.overlay_show_at(x, y)
	log.Printf("[overlay] show @%.0f,%.0f caret=%v", float64(x), float64(y), caretOK)
}

func hide() {
	C.overlay_hide()
	log.Printf("[overlay] hide")
}

// SelfTest 自检:显示 3 秒(voice-input overlaytest 子命令,CLI 下手动驱动 runloop)。
func SelfTest() (caretOK bool) {
	C.overlay_init()
	var x, y C.double
	caretOK = C.overlay_caret_position(&x, &y) == 1
	if !caretOK {
		C.overlay_fallback_position(&x, &y)
	}
	C.overlay_show_at(x, y)
	C.overlay_run_loop_for(0.5)
	log.Printf("[overlay] 自检:位置 %.0f,%.0f caret=%v panelVisible=%d",
		float64(x), float64(y), caretOK, C.overlay_panel_visible())
	C.overlay_run_loop_for(2.5)
	C.overlay_hide()
	C.overlay_run_loop_for(0.2)
	return caretOK
}
