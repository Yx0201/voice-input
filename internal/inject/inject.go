// Package inject 把文本"打"到当前聚焦的输入框。
// 实现走 macOS CGEvent 的 Unicode 键盘事件(CGEventKeyboardSetUnicodeString),
// 与 robotgo.TypeStr 同一底层机制,但零第三方依赖。
// 需要 辅助功能 权限(系统设置 → 隐私与安全性 → 辅助功能)。
package inject

/*
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation -framework ApplicationServices
#include <CoreGraphics/CGEvent.h>
#include <ApplicationServices/ApplicationServices.h>

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
// CGEvent 单事件携带的 Unicode 有实践上限,按 20 字符分块投递。
func TypeText(s string) {
	runes := []rune(s)
	const chunk = 20
	for i := 0; i < len(runes); i += chunk {
		end := i + chunk
		if end > len(runes) {
			end = len(runes)
		}
		part := runes[i:end]
		buf := make([]uint16, len(part))
		for j, r := range part {
			buf[j] = uint16(r)
		}
		C.postUnicodeChunk((*C.uint16_t)(&buf[0]), C.int(len(buf)))
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
