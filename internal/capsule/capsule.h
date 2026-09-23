#ifndef VOICE_INPUT_CAPSULE_H
#define VOICE_INPUT_CAPSULE_H

// 显示胶囊(录音态:波形;位置自动摆到主屏底部中央、Dock 上方)
void capsule_show(void);
// 隐藏胶囊
void capsule_hide(void);
// 推送一帧音量(0..1),波形条向左滚动(线程安全,可在音频线程调用)
void capsule_set_level(double v);
// 切换状态:1=转换中(中央 loading 圈,波形隐藏) 0=录音(波形)
void capsule_set_converting(int on);
// 胶囊当前是否可见(调试自检)
int capsule_panel_visible(void);
// 在当前线程驱动主 runloop 秒数(CLI 自检用)
void capsule_run_loop_for(double seconds);

#endif
