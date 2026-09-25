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
	// DashScopeAPIKey 百炼密钥;可存钥匙串(菜单切换云端时弹窗输入后自动保存)。
	DashScopeAPIKey string `json:"dashscope_api_key"`
	// DashScopeWorkspaceID 百炼业务空间 ID(可选);配置后走专属端点。
	DashScopeWorkspaceID string `json:"dashscope_workspace_id"`
	// DashScopeModel 云端 ASR 模型名。
	DashScopeModel string `json:"dashscope_model"`
	// Region 百炼地域。
	Region string `json:"region"`
	// HotkeyModifiers 全局热键修饰键(ctrl/alt/shift/cmd),默认 ["ctrl","alt"]。
	HotkeyModifiers []string `json:"hotkey_modifiers"`
	// HotkeyKey 全局热键主键(字母/数字/"space"),默认 "v"。
	HotkeyKey string `json:"hotkey_key"`
	// HotkeyMode 触发方式:"toggle"(组合键切换,默认)/ "ptt"(按住说话)。
	HotkeyMode string `json:"hotkey_mode"`
	// PttModifiers 按住说话的修饰键(默认 ["option"])。
	PttModifiers []string `json:"ptt_modifiers"`
	// PttKey 按住说话的主键(默认 "space")。
	PttKey string `json:"ptt_key"`
	// InputDevice 指定输入设备(设备名子串匹配,如 "AirPods");空 = 系统默认输入。
	InputDevice string `json:"input_device"`
	// DictationMode 听写模式:"streaming"(即时出字,默认)/ "sentence"(完整句,更准)。
	DictationMode string `json:"dictation_mode"`

	// PolishProvider 文字润色:"bailian"(默认开启)/ "off"(用户显式关闭)。
	// 仅云端单通道(2026-09-25 裁决);实际生效还需百炼 Key——无 Key 时静默失效
	// (输出原文),请求失败同样静默回退原文。
	PolishProvider string `json:"polish_provider"`
	// PolishModel 润色模型名;空 = qwen3.8-flash。
	PolishModel string `json:"polish_model"`
	// PolishMinChars 润色缓冲最小触发字数(句末标点时达到才冲刷),默认 20。
	PolishMinChars int `json:"polish_min_chars"`
	// PolishMaxChars 缓冲硬上限(达到即强制冲刷,哪怕在半句),默认 120。
	PolishMaxChars int `json:"polish_max_chars"`
	// PolishTimeoutMs 单次润色请求超时;0 = 3s。
	PolishTimeoutMs int `json:"polish_timeout_ms"`
	// OutputLanguage 输出语言:"zh"(默认,润色中文)/ "en"(清理并翻译成英文)。
	// en 模式下润色管线强制启用(需百炼 Key,无 Key 静默回退中文原文)。
	OutputLanguage string `json:"output_language"`
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
		HotkeyMode:      "toggle",
		PttModifiers:    []string{"option"},
		PttKey:          "space",
		DictationMode:   "streaming",
		PolishProvider:  "bailian",
		PolishMinChars:  20,
		PolishMaxChars:  120,
		OutputLanguage:  "zh",
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
	if v := os.Getenv("DASHSCOPE_WORKSPACE_ID"); v != "" {
		cfg.DashScopeWorkspaceID = v
	}
	if v := os.Getenv("VOICE_INPUT_DICTATION_MODE"); v != "" {
		cfg.DictationMode = v
	}
	if v := os.Getenv("VOICE_INPUT_POLISH_PROVIDER"); v != "" {
		cfg.PolishProvider = v
	}
	if v := os.Getenv("VOICE_INPUT_POLISH_MODEL"); v != "" {
		cfg.PolishModel = v
	}
	if v := os.Getenv("VOICE_INPUT_OUTPUT_LANGUAGE"); v != "" {
		cfg.OutputLanguage = v
	}
	return cfg
}

// PatchConfig 修改 ~/.voice_input/config.json 中的指定字段(文件不存在则创建),
// 用于持久化菜单里的运行时选择(如引擎切换)。
func PatchConfig(patch map[string]any) error {
	path := filepath.Join(DefaultDir(), "config.json")
	m := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &m) // 解析失败则从空配置开始
	}
	for k, v := range patch {
		m[k] = v
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(DefaultDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
