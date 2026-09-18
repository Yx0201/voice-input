// 全局键盘事件监听(CGEventTap):系统级监听按键,不依赖窗口焦点。
// 需要辅助功能权限;创建失败即返回错误(未授权)。
#include "tap.h"
#include <CoreGraphics/CGEvent.h>
#include <CoreFoundation/CFRunLoop.h>
#include <CoreFoundation/CFMachPort.h>

extern void htOnPress(void); // Go 侧实现(//export)

static CFMachPortRef ht_tap = NULL;
static unsigned long long ht_mask = 0;
static int ht_keycode = 0;

static CGEventRef ht_callback(CGEventTapProxy proxy, CGEventType type,
                              CGEventRef event, void *userInfo) {
    (void)proxy;
    (void)userInfo;
    // 系统超时可能临时禁用 tap,立即重新启用
    if (type == kCGEventTapDisabledByTimeout || type == kCGEventTapDisabledByUserInput) {
        if (ht_tap) {
            CGEventTapEnable(ht_tap, true);
        }
        return NULL;
    }
    if (type != kCGEventKeyDown) {
        return event;
    }

    CGEventFlags flags = CGEventGetFlags(event);
    CGKeyCode code = (CGKeyCode)CGEventGetIntegerValueField(event, kCGKeyboardEventKeycode);

    // 必须按下全部指定修饰键;未指定的 cmd/shift 不得出现(避免吞掉其他软件的组合键)
    unsigned long long forbidden = ((kCGEventFlagMaskCommand | kCGEventFlagMaskShift)) & ~ht_mask;
    if (code == (CGKeyCode)ht_keycode && (flags & ht_mask) == ht_mask &&
        !(flags & forbidden)) {
        htOnPress();
    }
    return event;
}

// 创建事件 tap 并挂到当前线程的 runloop;失败返回 -1(通常是辅助功能未授权)。
int ht_create_tap(unsigned long long mask, int keycode) {
    ht_mask = mask;
    ht_keycode = keycode;

    CGEventMask evMask = CGEventMaskBit(kCGEventKeyDown);
    ht_tap = CGEventTapCreate(kCGSessionEventTap, kCGHeadInsertEventTap,
                              kCGEventTapOptionDefault, evMask, ht_callback, NULL);
    if (!ht_tap) {
        return -1;
    }
    CFRunLoopSourceRef src = CFMachPortCreateRunLoopSource(kCFAllocatorDefault, ht_tap, 0);
    if (!src) {
        CFRelease(ht_tap);
        ht_tap = NULL;
        return -1;
    }
    CFRunLoopAddSource(CFRunLoopGetCurrent(), src, kCFRunLoopCommonModes);
    return 0;
}

// 阻塞运行当前线程的 runloop(事件回调在此线程触发)。
void ht_run_loop(void) {
    CGEventTapEnable(ht_tap, true);
    CFRunLoopRun();
}
