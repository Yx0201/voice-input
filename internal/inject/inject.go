// Package inject 把文本"打"到当前聚焦的输入框。
// 实现走 macOS CGEvent 的 Unicode 键盘事件(CGEventKeyboardSetUnicodeString),
// 与 robotgo.TypeStr 同一底层机制,但零第三方依赖。
// 需要 辅助功能 权限(系统设置 → 隐私与安全性 → 辅助功能)。
package inject

/*
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation -framework ApplicationServices
#include <CoreGraphics/CGEvent.h>
#include <ApplicationServices/ApplicationServices.h>
#include <unistd.h>

// postReturnKey 发一次带 Shift 修饰的回车(Shift+Enter 在备忘录/网页输入/
// Chromium 聊天框里都是"换行"而非"发送";裸 \n 字符注入在聊天应用会触发发送)。
static void postReturnKey(void) {
	CGEventFlags flags = kCGEventFlagMaskShift;
	CGEventRef down = CGEventCreateKeyboardEvent(NULL, 0x24, true); // kVK_Return
	CGEventRef up   = CGEventCreateKeyboardEvent(NULL, 0x24, false);
	CGEventSetFlags(down, flags);
	CGEventSetFlags(up, flags);
	CGEventPost(kCGSessionEventTap, down);
	CGEventPost(kCGSessionEventTap, up);
	CFRelease(down);
	CFRelease(up);
	usleep(12000); // 给目标应用的处理留一点节奏
}

// postUnicodeChunk 以一次按键事件携带一段 Unicode 文本(≤20 字符)投递到会话。
static void postUnicodeChunk(const uint16_t *text, int length) {
	CGEventRef down = CGEventCreateKeyboardEvent(NULL, 0, true);
	CGEventRef up   = CGEventCreateKeyboardEvent(NULL, 0, false);
	CGEventKeyboardSetUnicodeString(down, length, text);
	CGEventKeyboardSetUnicodeString(up, length, text);
	CGEventPost(kCGSessionEventTap, down);
	CGEventPost(kCGSessionEventTap, up);
	CFRelease(down);
	CFRelease(up);
}

// postBackspaces 连发 n 次退格(删除键,kVK_Delete=0x33),带小间隔防漏。
static void postBackspaces(int n) {
	for (int i = 0; i < n; i++) {
		CGEventRef down = CGEventCreateKeyboardEvent(NULL, 0x33, true);
		CGEventRef up   = CGEventCreateKeyboardEvent(NULL, 0x33, false);
		CGEventPost(kCGSessionEventTap, down);
		CGEventPost(kCGSessionEventTap, up);
		CFRelease(down);
		CFRelease(up);
		usleep(8000); // 8ms,与应用的按键处理节奏匹配
	}
}

// requestAccessibility 返回当前是否已信任;prompt 非零时让系统弹出授权引导。
static int checkAccessibility(int prompt) {
	if (!prompt) {
		return AXIsProcessTrusted() ? 1 : 0;
	}
	CFBooleanRef v = kCFBooleanTrue;
	CFStringRef key = kAXTrustedCheckOptionPrompt;
	CFDictionaryRef options = CFDictionaryCreate(
		kCFAllocatorDefault,
		(const void **)&key, (const void **)&v, 1,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	Boolean trusted = AXIsProcessTrustedWithOptions(options);
	CFRelease(options);
	return trusted ? 1 : 0;
}
*/
import "C"

// TypeText 把整段文本打进焦点输入框。
// 文本中的换行符不作为字符注入(聊天应用会触发发送、部分应用忽略),
// 而是拆分文本:段间发一次 Shift+Return 键事件(见 postReturnKey 注释)。
// 其余文本按 20 字符分块投递;非 BMP 字符(emoji 等)编码为 UTF-16 代理对。
func TypeText(s string) {
	runes := []rune(s)
	const chunk = 20
	start := 0
	flush := func(part []rune) {
		if len(part) == 0 {
			return
		}
		buf := make([]uint16, 0, len(part)*2)
		for _, r := range part {
			if r > 0xFFFF {
				r1 := r - 0x10000
				buf = append(buf,
					uint16(0xD800+(r1>>10)),
					uint16(0xDC00+(r1&0x3FF)))
			} else {
				buf = append(buf, uint16(r))
			}
		}
		C.postUnicodeChunk((*C.uint16_t)(&buf[0]), C.int(len(buf)))
	}
	for i, r := range runes {
		if r == '\n' {
			flush(runes[start:i])
			C.postReturnKey()
			start = i + 1
		}
	}
	flush(runes[start:])
}

// Backspaces 向焦点输入框连发 n 次退格(撤销上一句用)。
func Backspaces(n int) {
	if n <= 0 {
		return
	}
	C.postBackspaces(C.int(n))
}

// RequestAccessibility 检查辅助功能权限;未授权时触发系统弹窗引导用户去设置。
func RequestAccessibility() bool {
	return C.checkAccessibility(1) == 1
}

// IsAccessibilityGranted 只检查辅助功能权限状态,不弹任何窗。
func IsAccessibilityGranted() bool {
	return C.checkAccessibility(0) == 1
}
