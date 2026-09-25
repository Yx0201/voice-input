#ifndef VOICE_INPUT_HOTKEY_TAP_H
#define VOICE_INPUT_HOTKEY_TAP_H

// 一个 tap 的最大观察位数(slot0=听写热键,slot1=附加热键如撤销)
#define HT_MAX_WATCHERS 2

int ht_create_tap(unsigned long long mask, int keycode, int consume);
int ht_add_watcher(unsigned long long mask, int keycode);
void ht_run_loop(void);
void ht_stop_loop(void);

#endif
