// Package keystore 用 macOS 钥匙串(keychain)安全保存云端 API 密钥。
// 钥匙串本身即系统级加密存储(登录密码保护、AES 加密落盘),
// 首次读取时系统可能弹一次"允许 security 访问"——点「始终允许」即可。
package keystore

import (
	"os/exec"
	"strings"
)

const (
	service = "voice-input"
	account = "dashscope"
)

// Save 保存(或覆盖)百炼 API Key。
func Save(apiKey string) error {
	return exec.Command("security", "add-generic-password",
		"-s", service, "-a", account, "-w", apiKey, "-U").Run()
}

// Load 读取已保存的 Key;没有则返回空串。
func Load() string {
	out, err := exec.Command("security", "find-generic-password",
		"-s", service, "-a", account, "-w").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Delete 删除已保存的 Key(切换账号用)。
func Delete() error {
	return exec.Command("security", "delete-generic-password",
		"-s", service, "-a", account).Run()
}
