// Package asr 定义语音识别引擎的统一接口与实现。
// 本地引擎:sherpa-onnx + SenseVoice-Small(进程内)。
// 云端引擎:阿里百炼 qwen-audio-3.0-asr-flash(DashScope 原生接口)。
package asr

import (
	"fmt"

	"voice_input/internal/config"
)

// Engine 是 ASR 引擎的统一抽象:输入一段 PCM,返回识别文本。
// 引擎实现应为有状态(持有模型/客户端),由调用方长期复用。
type Engine interface {
	// Name 引擎名,用于日志与菜单栏展示。
	Name() string
	// Transcribe 转写一段完整音频。pcm 为 16-bit 源归一化后的 float32(±1.0),
	// 采样率由参数给出(本地引擎内部重采样到模型要求的 16kHz)。
	Transcribe(pcm []float32, sampleRate int) (string, error)
}

// NewEngine 按配置构造引擎,是本包对外的唯一入口。
func NewEngine(cfg config.Config) (Engine, error) {
	switch cfg.Engine {
	case "cloud":
		return NewCloudEngine(cfg)
	case "local":
		return NewLocalEngine(cfg)
	default:
		return nil, fmt.Errorf("未知引擎 %q(可选:local / cloud)", cfg.Engine)
	}
}
