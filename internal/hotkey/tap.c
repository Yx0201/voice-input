// 全局键盘事件监听(CGEventTap):系统级监听按键,不依赖窗口焦点。
// 一个 tap,两个观察位:
//   slot 0(主):听写热键。toggle=按下回调放行;ptt=按下/抬起分别回调且
//     命中事件被吞掉(返回 NULL)——按键不会到达任何应用,行为等同系统级
//     快捷键;配对保护:吞下的 keyDown 其 keyUp 无论修饰键状态都吞,防泄漏。
//   slot 1(副):附加热键(如撤销上一句),toggle 语义,事件放行。
// 需要辅助功能权限;创建失败即返回错误(未授权)。
#include "tap.h"
#include <CoreGraphics/CGEvent.h>
#include <CoreFoundation/CFRunLoop.h>
#include <CoreFoundation/CFMachPort.h>

extern void htOnPress(void);   // slot0 keyDown 命中(toggle/ptt 都回调)
extern void htOnRelease(void); // slot0 keyUp 命中(仅 ptt 回调)
extern void ht2OnPress(void);  // slot1 keyDown 命中(放行式)

typedef struct {
    unsigned long long mask;
    int keycode;
    int consume;  // 1=吞掉命中事件(ptt,仅 slot0 使用)
    int pairing;  // 已吞 keyDown,等待配对 keyUp
    int active;   // 该观察位是否启用
} ht_watcher;

static ht_watcher ht_w[HT_MAX_WATCHERS];
static CFMachPortRef ht_tap = NULL;
static CFRunLoopRef ht_runloop = NULL;

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

    for (int i = 0; i < HT_MAX_WATCHERS; i++) {
        ht_watcher *w = &ht_w[i];
        if (!w->active || code != (CGKeyCode)w->keycode) {
            continue;
        }
        // 未指定的 cmd/shift 不得出现(避免吞掉/误触其他软件的组合键)
        unsigned long long forbidden = ((kCGEventFlagMaskCommand | kCGEventFlagMaskShift)) & ~w->mask;
        if ((flags & w->mask) != w->mask || (flags & forbidden)) {
            continue;
        }
        if (w->consume) { // ptt:吞掉,不到达任何应用
            w->pairing = 1;
            if (type == kCGEventKeyDown) {
                htOnPress();
            } else {
                w->pairing = 0;
                htOnRelease();
            }
            return NULL;
        }
        if (type == kCGEventKeyDown) { // toggle:按下回调,放行
            if (i == 0) {
                htOnPress();
            } else {
                ht2OnPress();
            }
        }
        return event;
    }

    // 配对保护:已吞下按下的键,其抬起即使修饰键已松开也必须吞,否则会漏出一次空格/字符
    for (int i = 0; i < HT_MAX_WATCHERS; i++) {
        ht_watcher *w = &ht_w[i];
        if (w->active && w->consume && w->pairing && code == (CGKeyCode)w->keycode) {
            if (type == kCGEventKeyUp) {
                w->pairing = 0;
                htOnRelease();
            }
            return NULL;
        }
    }
    return event;
}

// 创建事件 tap 并挂到当前线程的 runloop;失败返回 -1(通常是辅助功能未授权)。
// 只注册 slot0;slot1 由 ht_add_watcher 补充(可在 tap 运行中随时添加)。
int ht_create_tap(unsigned long long mask, int keycode, int consume) {
    ht_w[0] = (ht_watcher){mask, keycode, consume, 0, 1};

    CGEventMask evMask = CGEventMaskBit(kCGEventKeyDown);
    if (consume) {
        evMask |= CGEventMaskBit(kCGEventKeyUp); // ptt 需要抬起事件
    }
    ht_tap = CGEventTapCreate(kCGSessionEventTap, kCGHeadInsertEventTap,
                              kCGEventTapOptionDefault, evMask, ht_callback, NULL);
    if (!ht_tap) {
        ht_w[0].active = 0;
        return -1;
    }
    CFRunLoopSourceRef src = CFMachPortCreateRunLoopSource(kCFAllocatorDefault, ht_tap, 0);
    if (!src) {
        CFRelease(ht_tap);
        ht_tap = NULL;
        ht_w[0].active = 0;
        return -1;
    }
    CFRunLoopAddSource(CFRunLoopGetCurrent(), src, kCFRunLoopCommonModes);
    return 0;
}

// 注册/更新 slot1 附加观察位(toggle 语义,放行);返回 0 成功。
int ht_add_watcher(unsigned long long mask, int keycode) {
    ht_w[1] = (ht_watcher){mask, keycode, 0, 0, 1};
    return 0;
}

// 阻塞运行当前线程的 runloop(事件回调在此线程触发)。
void ht_run_loop(void) {
    CGEventTapEnable(ht_tap, true);
    ht_runloop = CFRunLoopGetCurrent();
    CFRunLoopRun();
}

// 停止 runloop(用于热重配/模式切换);监听线程会从 ht_run_loop 返回。
// 注意:slot1 配置不因 tap 重建丢失(由 Go 侧在每次 create 后重新 add)。
void ht_stop_loop(void) {
    if (ht_runloop) {
        CFRunLoopStop(ht_runloop);
        ht_runloop = NULL;
    }
}
