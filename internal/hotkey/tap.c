// 全局键盘事件监听(CGEventTap):系统级监听按键,不依赖窗口焦点。
// 两种工作模式:
//   toggle:监听 keyDown,命中即回调,事件照常放行(不吞)
//   ptt(按住说话):监听 keyDown+keyUp,命中事件被吞掉(返回 NULL)——
//     按键不会到达任何应用(不会打出空格/字符),行为等同系统级快捷键;
//     配对保护:吞下的 keyDown,其 keyUp 无论修饰键状态如何都吞,防字符泄漏。
// 需要辅助功能权限;创建失败即返回错误(未授权)。
#include "tap.h"
#include <CoreGraphics/CGEvent.h>
#include <CoreFoundation/CFRunLoop.h>
#include <CoreFoundation/CFMachPort.h>

extern void htOnPress(void);   // keyDown 命中(toggle/ptt 都回调)
extern void htOnRelease(void); // keyUp 命中(仅 ptt 模式回调)

static CFMachPortRef ht_tap = NULL;
static CFRunLoopRef ht_runloop = NULL;
static unsigned long long ht_mask = 0;
static int ht_keycode = 0;
static int ht_consume = 0; // 1=吞掉命中事件(ptt)
static int ht_pairing = 0; // 已吞 keyDown,等待配对 keyUp

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
    if (type != kCGEventKeyDown && type != kCGEventKeyUp) {
        return event;
    }

    CGEventFlags flags = CGEventGetFlags(event);
    CGKeyCode code = (CGKeyCode)CGEventGetIntegerValueField(event, kCGKeyboardEventKeycode);

    // 未指定的 cmd/shift 不得出现(避免吞掉其他软件的组合键)
    unsigned long long forbidden = ((kCGEventFlagMaskCommand | kCGEventFlagMaskShift)) & ~ht_mask;

    if (code == (CGKeyCode)ht_keycode && (flags & ht_mask) == ht_mask &&
        !(flags & forbidden)) {
        if (ht_consume) {
            ht_pairing = 1;
            if (type == kCGEventKeyDown) {
                htOnPress();
            } else {
                ht_pairing = 0;
                htOnRelease();
            }
            return NULL; // 吞掉,不到达任何应用
        }
        if (type == kCGEventKeyDown) { // toggle 模式只在按下时回调,且放行
            htOnPress();
        }
        return event;
    }

    // 配对保护:已吞下按下的键,其抬起即使修饰键已松开也必须吞,否则会漏出一次空格/字符
    if (ht_consume && ht_pairing && code == (CGKeyCode)ht_keycode) {
        if (type == kCGEventKeyUp) {
            ht_pairing = 0;
            htOnRelease();
        }
        return NULL;
    }
    return event;
}

// 创建事件 tap 并挂到当前线程的 runloop;失败返回 -1(通常是辅助功能未授权)。
int ht_create_tap(unsigned long long mask, int keycode, int consume) {
    ht_mask = mask;
    ht_keycode = keycode;
    ht_consume = consume;
    ht_pairing = 0;

    CGEventMask evMask = CGEventMaskBit(kCGEventKeyDown);
    if (consume) {
        evMask |= CGEventMaskBit(kCGEventKeyUp); // ptt 需要抬起事件
    }
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
    ht_runloop = CFRunLoopGetCurrent();
    CFRunLoopRun();
}

// 停止 runloop(用于热重配/模式切换);监听线程会从 ht_run_loop 返回。
void ht_stop_loop(void) {
    if (ht_runloop) {
        CFRunLoopStop(ht_runloop);
        ht_runloop = NULL;
    }
}
