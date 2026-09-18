package asr

import (
	"fmt"

	"voice_input/internal/config"
)

// CloudEngine 基于阿里百炼 qwen-audio-3.0-asr-flash 的云端引擎。
//
// 注意:qwen-audio 系列**不走** OpenAI 兼容的 /v1/audio/transcriptions
// (该端点仅支持 qwen3-asr-flash 系列),需用 DashScope 原生多模态接口,
// 音频以 base64 data URL 形式放入消息内容。实现时对照百炼文档:
// https://help.aliyun.com/zh/model-studio/ 非实时语音识别。
type CloudEngine struct {
	cfg config.Config
}

// NewCloudEngine 构造云端引擎(下一里程碑实现 HTTP 调用)。
func NewCloudEngine(cfg config.Config) (*CloudEngine, error) {
	if cfg.DashScopeAPIKey == "" {
		return nil, fmt.Errorf("云端引擎需要密钥:设置 DASHSCOPE_API_KEY 环境变量," +
			"或在 ~/.voice_input/config.json 的 dashscope_api_key 填写(可复用 chat-elf 的 key)")
	}
	return &CloudEngine{cfg: cfg}, nil
}

// Name 实现 Engine 接口。
func (e *CloudEngine) Name() string {
	return fmt.Sprintf("cloud(%s)", e.cfg.DashScopeModel)
}

// Transcribe 实现 Engine 接口。
func (e *CloudEngine) Transcribe(pcm []float32, sampleRate int) (string, error) {
	return "", fmt.Errorf("云端引擎尚未实现(里程碑 3):" +
		"将 PCM 编码为 WAV → base64 → DashScope 原生多模态接口 POST %s", e.endpoint())
}

// endpoint 返回百炼原生接口地址(区域化)。
func (e *CloudEngine) endpoint() string {
	return fmt.Sprintf("https://dashscope.%s.aliyuncs.com/api/v1/services/audio/asr", e.cfg.Region)
}
