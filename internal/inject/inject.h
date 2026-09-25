#ifndef VOICE_INPUT_INJECT_H
#define VOICE_INPUT_INJECT_H

#include <stddef.h>

void postCmdV(void);
void clipPut(const char *utf8);
char *clipGet(void); // 返回 malloc 字符串,调用方 free;无文本返回 NULL
void clipClear(void);
void postBackspaces(int n);
int checkAccessibility(int prompt);

#endif
