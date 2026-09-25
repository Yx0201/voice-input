// 文本注入的 ObjC 部分:剪贴板读写(AppKit)+ 键事件投递。
// 之所以放伴生 .m 而非 cgo 前导块:前导块按纯 C 编译,NSPasteboard 语法不进。
#include "inject.h"
#include <CoreGraphics/CGEvent.h>
#include <ApplicationServices/ApplicationServices.h>
#include <AppKit/AppKit.h>
#include <unistd.h>
#include <stdlib.h>
#include <string.h>

// postCmdV 发一次 Cmd+V。
void postCmdV(void) {
	CGEventFlags cmd = kCGEventFlagMaskCommand;
	CGEventRef d = CGEventCreateKeyboardEvent(NULL, 0x09, true); // kVK_ANSI_V
	CGEventRef u = CGEventCreateKeyboardEvent(NULL, 0x09, false);
	CGEventSetFlags(d, cmd);
	CGEventSetFlags(u, cmd);
	CGEventPost(kCGSessionEventTap, d);
	CGEventPost(kCGSessionEventTap, u);
	CFRelease(d);
	CFRelease(u);
}

// clipPut 把文本写入系统剪贴板(先清空)。
void clipPut(const char *utf8) {
	NSString *s = [NSString stringWithUTF8String:utf8];
	NSPasteboard *pb = [NSPasteboard generalPasteboard];
	[pb clearContents];
	[pb setString:s forType:NSPasteboardTypeString];
}

// clipGet 读剪贴板纯文本;无文本返回 NULL。调用方 free。
char *clipGet(void) {
	NSString *s = [[NSPasteboard generalPasteboard] stringForType:NSPasteboardTypeString];
	if (!s) {
		return NULL;
	}
	return strdup([s UTF8String]);
}

// clipClear 清空剪贴板。
void clipClear(void) {
	[[NSPasteboard generalPasteboard] clearContents];
}

// postBackspaces 连发 n 次退格(删除键,kVK_Delete=0x33),带节奏防丢。
void postBackspaces(int n) {
	for (int i = 0; i < n; i++) {
		CGEventRef down = CGEventCreateKeyboardEvent(NULL, 0x33, true);
		CGEventRef up   = CGEventCreateKeyboardEvent(NULL, 0x33, false);
		CGEventPost(kCGSessionEventTap, down);
		CGEventPost(kCGSessionEventTap, up);
		CFRelease(down);
		CFRelease(up);
		usleep(12000);
	}
}

// checkAccessibility 返回当前是否已信任;prompt 非零时让系统弹出授权引导。
int checkAccessibility(int prompt) {
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
