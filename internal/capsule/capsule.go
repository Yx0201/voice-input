// Package capsule 屏幕底部中央的"声音胶囊"状态悬浮层:
// 按住说话/开启听写 → 胶囊出现并显示实时波形;
// 停止说话、AI 转换在途 → 胶囊内显示 loading 圈;
// 全部转换结束 → 胶囊消失,等待下一次收音。
package capsule

/*
#cgo LDFLAGS: -framework AppKit -framework Foundation
#include "capsule.h"
*/
import "C"

import (
	"log"
	"math"
	"sync"
	"time"
)

// hideDelay 松手后(无在途转换时)或全部转换完成后的短暂停留,
// 吸收转换任务的调度间隙,防止胶囊闪烁消失。
const hideDelay = 500 * time.Millisecond

var (
	mu        sync.Mutex
	recording bool
	converts  int // 在途转换计数(可重入)
	hideT     *time.Timer
	lastLvl   float64
)

// Begin 开始录音:胶囊出现并显示波形。
func Begin() {
	mu.Lock()
	recording = true
	lastLvl = 0
	if hideT != nil {
		hideT.Stop()
		hideT = nil
	}
	mu.Unlock()
	C.capsule_show()
	log.Printf("[capsule] 录音开始(波形)")
}

// Level 推送一帧音量(0..1,线性 RMS 归一化),驱动波形起伏。
// 快起慢落:有声立刻顶上去,停顿后约 150ms 渐落,视觉更跟手。
func Level(v float64) {
	if v < 0 {
		v = 0
	} else if v > 1 {
		v = 1
	}
	mu.Lock()
	if v < lastLvl {
		v = math.Max(v, lastLvl*0.68)
	}
	lastLvl = v
	mu.Unlock()
	C.capsule_set_level(C.double(v))
}

// End 录音结束(关麦):有在途转换则胶囊切换为 loading 态,否则延迟隐藏。
func End() {
	mu.Lock()
	recording = false
	mu.Unlock()
	evaluate()
}

// BeginConvert 标记一段识别在途(可重入,返回结束函数)。
// 录音进行中只计数不改画面(波形优先);录音结束后由它撑起 loading 态。
func BeginConvert() func() {
	mu.Lock()
	converts++
	if hideT != nil {
		hideT.Stop()
		hideT = nil
	}
	mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			mu.Lock()
			converts--
			mu.Unlock()
			evaluate()
		})
	}
}

// evaluate 依据 recording/converts 决定胶囊画面:录音→波形(不动);
// 无录音有转换→loading;两者皆无→延迟隐藏。
func evaluate() {
	mu.Lock()
	defer mu.Unlock()
	if recording {
		return
	}
	if converts > 0 {
		C.capsule_set_converting(1)
		return
	}
	if hideT != nil {
		return
	}
	hideT = time.AfterFunc(hideDelay, func() {
		mu.Lock()
		defer mu.Unlock()
		hideT = nil
		if !recording && converts <= 0 {
			C.capsule_hide()
			log.Printf("[capsule] 全部转换完成,胶囊隐藏")
		}
	})
}

// SelfTest CLI 自检(capsuletest 子命令):波形 2s → 转换 1.2s → 隐藏。
func SelfTest() {
	log.SetFlags(log.Ltime)
	Begin()
	for i := 0; i < 100; i++ { // 2 秒模拟语音音量:正弦起伏
		Level(0.15 + 0.8*math.Abs(math.Sin(float64(i)*0.22)))
		C.capsule_run_loop_for(0.02)
	}
	log.Printf("[capsule] 自检:波形阶段 panelVisible=%d(应为 1)", C.capsule_panel_visible())
	done := BeginConvert()
	End()
	C.capsule_run_loop_for(1.2)
	log.Printf("[capsule] 自检:转换阶段 panelVisible=%d(应为 1)", C.capsule_panel_visible())
	done()
	C.capsule_run_loop_for(0.6)
	log.Printf("[capsule] 自检:结束后 panelVisible=%d(应为 0)", C.capsule_panel_visible())
}
