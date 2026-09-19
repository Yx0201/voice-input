#ifndef VOICE_INPUT_OVERLAY_SPINNER_H
#define VOICE_INPUT_OVERLAY_SPINNER_H

// 初始化悬浮层(幂等;须在有 AppKit runloop 的进程内调用,即 app 模式)
void overlay_init(void);
// 在屏幕坐标(AppKit 原点,左下)显示 spinner
void overlay_show_at(double x, double y);
// 隐藏
void overlay_hide(void);
// 取当前焦点文本框光标的屏幕坐标(AppKit 原点);成功返回 1
int overlay_caret_position(double *x, double *y);
// 最近一次光标查询的诊断信息(失败步骤/AX 错误码)
const char *overlay_caret_debug(void);
// 兜底位置(主屏右下角,AppKit 原点)
void overlay_fallback_position(double *x, double *y);
// 在当前线程驱动主 runloop 秒数(CLI 自检用:让 dispatch 的 UI 任务得以执行)
void overlay_run_loop_for(double seconds);
// 悬浮窗当前是否可见(调试自检)
int overlay_panel_visible(void);

#endif
