// 声音胶囊:屏幕底部中央(Dock 上方)的状态悬浮层。
// 录音态 = 随音量起伏的波形条;转换态 = 中央原生 loading 圈。
// 透明·置顶·不抢焦点·可穿透;主线程直调,其他线程派发主队列。
#import <AppKit/AppKit.h>
#include "capsule.h"
#include <pthread.h>

static const CGFloat kCapsuleW = 200, kCapsuleH = 44;
static const int kBars = 16;
static const CGFloat kBarW = 6, kBarGap = 5;

// 波形环形缓冲:barHead 指向"下一帧写入位",读出时旧→新 从左到右
static pthread_mutex_t lvlMu = PTHREAD_MUTEX_INITIALIZER;
static float barLevels[kBars];
static int barHead = 0;
static BOOL converting = NO;

@interface WaveView : NSView
@end

@implementation WaveView
- (void)drawRect:(NSRect)dirtyRect {
	NSRect b = self.bounds;
	// 胶囊底:深色半透明圆角(半径=高/2,即胶囊形)
	[[NSColor colorWithCalibratedWhite:0.07 alpha:0.72] setFill];
	NSBezierPath *bg = [NSBezierPath bezierPathWithRoundedRect:b
		xRadius:b.size.height / 2 yRadius:b.size.height / 2];
	[bg fill];

	if (converting) return; // 转换态:只有底 + 中央 spinner(独立子视图)

	float lv[kBars];
	pthread_mutex_lock(&lvlMu);
	for (int i = 0; i < kBars; i++) lv[i] = barLevels[(barHead + i) % kBars];
	pthread_mutex_unlock(&lvlMu);

	CGFloat totalW = kBars * kBarW + (kBars - 1) * kBarGap;
	CGFloat x = (b.size.width - totalW) / 2, midY = b.size.height / 2;
	CGFloat maxH = b.size.height - 14;
	for (int i = 0; i < kBars; i++, x += kBarW + kBarGap) {
		CGFloat h = 4 + lv[i] * maxH;
		if (h > maxH) h = maxH;
		NSRect r = NSMakeRect(x, midY - h / 2, kBarW, h);
		[[NSColor colorWithCalibratedWhite:1.0 alpha:0.92] setFill];
		NSBezierPath *p = [NSBezierPath bezierPathWithRoundedRect:r
			xRadius:kBarW / 2 yRadius:kBarW / 2];
		[p fill];
	}
}
@end

static NSPanel *capsulePanel = nil;
static WaveView *waveView = nil;
static NSProgressIndicator *spinIndicator = nil;

static void onMain(void (^block)(void)) {
	if ([NSThread isMainThread]) {
		block();
	} else {
		dispatch_async(dispatch_get_main_queue(), block);
	}
}

static void layoutPanel(void) {
	NSScreen *scr = [NSScreen mainScreen];
	if (!scr) return;
	CGFloat y = scr.visibleFrame.origin.y + 12; // Dock(或屏底)上方一点
	CGFloat x = scr.frame.origin.x + (scr.frame.size.width - kCapsuleW) / 2;
	[capsulePanel setFrameOrigin:NSMakePoint(x, y)];
}

static void ensurePanel(void) {
	if (capsulePanel) return;
	// 窗口渲染依赖 NSApplication(后台附件模式,不占 Dock)
	NSApplication *app = [NSApplication sharedApplication];
	[app setActivationPolicy:NSApplicationActivationPolicyAccessory];

	capsulePanel = [[NSPanel alloc] initWithContentRect:NSMakeRect(0, 0, kCapsuleW, kCapsuleH)
		styleMask:NSWindowStyleMaskBorderless backing:NSBackingStoreBuffered defer:NO];
	capsulePanel.opaque = NO;
	capsulePanel.backgroundColor = [NSColor clearColor];
	capsulePanel.hasShadow = YES;
	capsulePanel.hidesOnDeactivate = NO; // 后台应用永不激活,必须关掉"失活即隐藏"
	capsulePanel.ignoresMouseEvents = YES;
	capsulePanel.level = NSStatusWindowLevel;
	capsulePanel.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces
		| NSWindowCollectionBehaviorFullScreenAuxiliary;

	waveView = [[WaveView alloc] initWithFrame:NSMakeRect(0, 0, kCapsuleW, kCapsuleH)];
	capsulePanel.contentView = waveView;

	spinIndicator = [[NSProgressIndicator alloc]
		initWithFrame:NSMakeRect(kCapsuleW / 2 - 11, kCapsuleH / 2 - 11, 22, 22)];
	[spinIndicator setStyle:NSProgressIndicatorStyleSpinning];
	[spinIndicator setControlSize:NSControlSizeSmall];
	[spinIndicator setDisplayedWhenStopped:YES];
	[spinIndicator setUsesThreadedAnimation:YES];
	[spinIndicator setHidden:YES];
	[waveView addSubview:spinIndicator];
}

void capsule_show(void) {
	onMain(^{
		ensurePanel();
		layoutPanel();
		converting = NO;
		[spinIndicator setHidden:YES];
		[spinIndicator stopAnimation:nil];
		pthread_mutex_lock(&lvlMu);
		for (int i = 0; i < kBars; i++) barLevels[i] = 0;
		barHead = 0;
		pthread_mutex_unlock(&lvlMu);
		[waveView setNeedsDisplay:YES];
		[capsulePanel orderFrontRegardless]; // 不激活、不抢焦点
	});
}

void capsule_set_level(double v) {
	if (v < 0) v = 0;
	if (v > 1) v = 1;
	pthread_mutex_lock(&lvlMu);
	barLevels[barHead] = (float)v;
	barHead = (barHead + 1) % kBars;
	pthread_mutex_unlock(&lvlMu);
	onMain(^{ [waveView setNeedsDisplay:YES]; });
}

void capsule_set_converting(int on) {
	onMain(^{
		if (!capsulePanel) return;
		converting = (on != 0);
		if (converting) {
			[spinIndicator setHidden:NO];
			[spinIndicator startAnimation:nil];
		} else {
			[spinIndicator setHidden:YES];
			[spinIndicator stopAnimation:nil];
		}
		[waveView setNeedsDisplay:YES];
	});
}

void capsule_hide(void) {
	onMain(^{ [capsulePanel orderOut:nil]; });
}

int capsule_panel_visible(void) {
	return capsulePanel && [capsulePanel isVisible] ? 1 : 0;
}

void capsule_run_loop_for(double seconds) {
	// 须在主线程调用(CLI 的 main goroutine 即主线程)
	[[NSRunLoop mainRunLoop] runUntilDate:[NSDate dateWithTimeIntervalSinceNow:seconds]];
}
