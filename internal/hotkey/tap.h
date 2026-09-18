#ifndef VOICE_INPUT_HOTKEY_TAP_H
#define VOICE_INPUT_HOTKEY_TAP_H

int ht_create_tap(unsigned long long mask, int keycode);
void ht_run_loop(void);

#endif
