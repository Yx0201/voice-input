// 光标旁 loading 悬浮层(AppKit):
// 透明·置顶·不抢焦点·可穿透的 NSPanel + 原生旋转指示器。
// 主线程直接执行,其他线程派发到主队列(主 runloop 由 systray 驱动)。
#import <AppKit/AppKit.h>
#import <ApplicationServices/ApplicationServices.h>
#include "spinner.h"

static NSPanel *spinnerPanel = nil;

static void onMain(void (^block)(void)) {
	if ([NSThread isMainThread]) {
		block();
	} else {
		dispatch_async(dispatch_get_main_queue(), block);
	}
}

static void ensurePanel(void) {
	if (spinnerPanel) return;
	// 窗口渲染依赖 NSApplication(后台附件模式,不占 Dock)
	NSApplication *app = [NSApplication sharedApplication];
	[app setActivationPolicy:NSApplicationActivationPolicyAccessory];

	spinnerPanel = [[NSPanel alloc] initWithContentRect:NSMakeRect(0, 0, 24, 24)
		styleMask:NSWindowStyleMaskBorderless backing:NSBackingStoreBuffered defer:NO];
	spinnerPanel.opaque = NO;
	spinnerPanel.backgroundColor = [NSColor clearColor];
	spinnerPanel.hasShadow = NO;
	spinnerPanel.ignoresMouseEvents = YES;
	spinnerPanel.hidesOnDeactivate = NO;
	spinnerPanel.level = NSStatusWindowLevel; // 菜单栏同级,压过普通窗口
	spinnerPanel.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces
		| NSWindowCollectionBehaviorFullScreenAuxiliary;

	NSProgressIndicator *spin = [[NSProgressIndicator alloc] initWithFrame:NSMakeRect(0, 0, 24, 24)];
	[spin setStyle:NSProgressIndicatorStyleSpinning];
	[spin setControlSize:NSControlSizeSmall];
	[spin setDisplayedWhenStopped:YES];
	[spin setUsesThreadedAnimation:YES];
	[spin startAnimation:nil];
	spinnerPanel.contentView = spin;
}

void overlay_init(void) {
	onMain(^{ ensurePanel(); });
}

void overlay_show_at(double x, double y) {
	onMain(^{
		ensurePanel();
		[spinnerPanel setFrameOrigin:NSMakePoint((CGFloat)x, (CGFloat)y)];
		[spinnerPanel orderFrontRegardless]; // 不激活、不抢焦点
	});
}

void overlay_hide(void) {
	onMain(^{
		if (spinnerPanel) [spinnerPanel orderOut:nil];
	});
}

// 焦点文本框内光标(选区)的屏幕矩形。AX 返回的是 CG 坐标(左上原点),
// 这里转换为 AppKit 坐标(左下原点)。
// caretDebug 记录最近一次查询失败在哪一步、AX 错误码(供 Go 侧日志排障)。
static char caretDebug[160] = "not-run";

static CGRect caretScreenRect(void) {
    CGRect rect = CGRectNull;
    AXUIElementRef sys = AXUIElementCreateSystemWide();
    if (!sys) { snprintf(caretDebug, sizeof caretDebug, "syswide=create-failed"); return rect; }

    AXUIElementRef focused = NULL;
    AXError e1 = AXUIElementCopyAttributeValue(sys, kAXFocusedUIElementAttribute, (CFTypeRef *)&focused);
    CFRelease(sys);
    if (e1 != kAXErrorSuccess || !focused) {
        snprintf(caretDebug, sizeof caretDebug, "focused-elem err=%d", (int)e1);
        return rect;
    }

    AXValueRef rangeVal = NULL;
    AXError e2 = AXUIElementCopyAttributeValue(focused, kAXSelectedTextRangeAttribute, (CFTypeRef *)&rangeVal);
    if (e2 != kAXErrorSuccess || !rangeVal) {
        snprintf(caretDebug, sizeof caretDebug, "selrange err=%d", (int)e2);
        CFRelease(focused);
        return rect;
    }

    AXValueRef boundsVal = NULL;
    AXError e3 = AXUIElementCopyParameterizedAttributeValue(focused,
        kAXBoundsForRangeParameterizedAttribute, rangeVal, (CFTypeRef *)&boundsVal);
    CFRelease(rangeVal);
    if (e3 != kAXErrorSuccess || !boundsVal) {
        snprintf(caretDebug, sizeof caretDebug, "bounds err=%d", (int)e3);
        CFRelease(focused);
        return rect;
    }
    AXValueGetValue(boundsVal, kAXValueCGRectType, &rect);
    CFRelease(boundsVal);
    CFRelease(focused);
    snprintf(caretDebug, sizeof caretDebug, "ok w=%.0f h=%.0f", rect.size.width, rect.size.height);
    return rect;
}

// 最近一次光标查询的诊断信息(失败步骤/错误码,或成功时的矩形尺寸)
const char *overlay_caret_debug(void) {
    return caretDebug;
}

int overlay_caret_position(double *x, double *y) {
    CGRect r = caretScreenRect();
    // 空选区(纯插入点)的矩形宽度为 0、只有行高——听写场景几乎总是这种,
    // 判定成功与否看高度;width=0 时 x 就取行首。
    if (CGRectIsNull(r) || r.size.height <= 0) return 0;
    CGFloat screenHeight = [NSScreen mainScreen].frame.size.height;
    // CG(左上原点) → AppKit(左下原点);放在光标右侧、垂直居中
    *x = (double)(r.origin.x + r.size.width + 6);
    *y = (double)(screenHeight - (r.origin.y + r.size.height) + r.size.height / 2 - 12);
    return 1;
}

void overlay_fallback_position(double *x, double *y) {
	NSRect vf = [[NSScreen mainScreen] visibleFrame];
	*x = (double)(vf.origin.x + vf.size.width - 36);
	*y = (double)(vf.origin.y + 12);
}

void overlay_run_loop_for(double seconds) {
	// 须在主线程调用(CLI 的 main goroutine 即主线程)
	[[NSRunLoop mainRunLoop] runUntilDate:[NSDate dateWithTimeIntervalSinceNow:seconds]];
}

int overlay_panel_visible(void) {
	return spinnerPanel && [spinnerPanel isVisible] ? 1 : 0;
}
