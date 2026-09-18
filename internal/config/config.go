// Package config 负责 voice_input 的运行时配置。
// 优先级:环境变量 > ~/.voice_input/config.json > 默认值。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config 是应用的完整配置。
type Config struct {
	// Engine 选择 ASR 引擎:"local"(sherpa-onnx + SenseVoice)或 "cloud"(百炼)。
	Engine string `json:"engine"`
	// ModelDir 覆盖本地模型目录,默认 ~/.voice_input/models/sense-voice。
	ModelDir string `json:"model_dir"`
	// VADDir 覆盖 VAD 模型目录,默认 ~/.voice_input/models/vad。
	VADDir string `json:"vad_dir"`
	// DashScopeAPIKey 百炼密钥;复用 chat-elf 的 key,也可从 DASHSCOPE_API_KEY 环境变量读。
	DashScopeAPIKey string `json:"dashscope_api_key"`
	// DashScopeModel 云端 ASR 模型名。
	DashScopeModel string `json:"dashscope_model"`
	// Region 百炼地域。
	Region string `json:"region"`
	// HotkeyModifiers 全局热键修饰键(ctrl/alt/shift/cmd),默认 ["ctrl","alt"]。
	HotkeyModifiers []string `json:"hotkey_modifiers"`
	// HotkeyKey 全局热键主键(字母/数字),默认 "v"。
	HotkeyKey string `json:"hotkey_key"`
	// InputDevice 指定输入设备(设备名子串匹配,如 "AirPods");空 = 系统默认输入。
	InputDevice string `json:"input_device"`
}

// DefaultDir 返回数据根目录 ~/.voice_input。
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		panic(fmt.Sprintf("无法确定用户主目录: %v", err))
	}
	return filepath.Join(home, ".voice_input")
}

// DefaultSenseVoiceDir 本地 ASR 模型目录。
func DefaultSenseVoiceDir() string {
	return filepath.Join(DefaultDir(), "models", "sense-voice")
}

// DefaultVADDir VAD 模型目录。
func DefaultVADDir() string {
	return filepath.Join(DefaultDir(), "models", "vad")
}

func defaultConfig() Config {
	return Config{
		Engine:          "local",
		ModelDir:        DefaultSenseVoiceDir(),
		VADDir:          DefaultVADDir(),
		DashScopeModel:  "qwen-audio-3.0-asr-flash",
		Region:          "cn-beijing",
		HotkeyModifiers: []string{"ctrl", "alt"},
		HotkeyKey:       "v",
	}
}

// Load 读取配置:先取默认值,再叠 config.json,最后叠环境变量。
func Load() Config {
	cfg := defaultConfig()

	if raw, err := os.ReadFile(filepath.Join(DefaultDir(), "config.json")); err == nil {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			fmt.Fprintf(os.Stderr, "警告: %s/config.json 解析失败(%v),使用默认配置\n", DefaultDir(), err)
			cfg = defaultConfig()
		}
	}

	if v := os.Getenv("VOICE_INPUT_ENGINE"); v != "" {
		cfg.Engine = v
	}
	if v := os.Getenv("DASHSCOPE_API_KEY"); v != "" {
		cfg.DashScopeAPIKey = v
	}
	if v := os.Getenv("VOICE_INPUT_MODEL_DIR"); v != "" {
		cfg.ModelDir = v
	}
	if v := os.Getenv("VOICE_INPUT_VAD_DIR"); v != "" {
		cfg.VADDir = v
	}
	return cfg
}
