package asr

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	// sherpa-onnx 官方 Go 绑定(v1.13+ 起绑定与 macOS 预编译 dylib 同模块分发,
	// 内嵌 x86_64/arm64 双架构库,导入即完成链接)。换平台时改用
	// sherpa-onnx-go-linux 等兄弟模块。
	sherpa_onnx "github.com/k2-fsa/sherpa-onnx-go-macos"

	"voice_input/internal/config"
)

// LocalEngine 基于 sherpa-onnx 的 SenseVoice-Small 本地引擎。
// 识别中文/英文/日文/韩文/粤语;UseInverseTextNormalization 开启后
// 直接输出带标点、数字规整的可读文本。
type LocalEngine struct {
	recognizer *sherpa_onnx.OfflineRecognizer
}

// NewLocalEngine 加载模型并构造引擎。模型文件缺失时返回带路径提示的错误。
func NewLocalEngine(cfg config.Config) (*LocalEngine, error) {
	modelPath := filepath.Join(cfg.ModelDir, "model.int8.onnx")
	tokensPath := filepath.Join(cfg.ModelDir, "tokens.txt")

	for _, p := range []string{modelPath, tokensPath} {
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("模型文件不存在: %s(先运行 make setup 下载)", p)
		}
	}

	conf := sherpa_onnx.OfflineRecognizerConfig{
		ModelConfig: sherpa_onnx.OfflineModelConfig{
			SenseVoice: sherpa_onnx.OfflineSenseVoiceModelConfig{
				Model:                       modelPath,
				Language:                    "", // 自动检测
				UseInverseTextNormalization: 1,  // 标点 + 数字规整
			},
			Tokens:     tokensPath,
			NumThreads: 2,
			Debug:      0,
			Provider:   "cpu",
		},
	}

	recognizer := sherpa_onnx.NewOfflineRecognizer(&conf)
	if recognizer == nil {
		return nil, errors.New("加载 SenseVoice 模型失败(sherpa-onnx 返回空识别器,请检查模型文件完整性)")
	}
	return &LocalEngine{recognizer: recognizer}, nil
}

// Name 实现 Engine 接口。
func (e *LocalEngine) Name() string { return "local(SenseVoice-Small)" }

// Transcribe 实现 Engine 接口:整段音频离线转写。
func (e *LocalEngine) Transcribe(pcm []float32, sampleRate int) (string, error) {
	if len(pcm) == 0 {
		return "", nil
	}
	stream := sherpa_onnx.NewOfflineStream(e.recognizer)
	stream.AcceptWaveform(sampleRate, pcm)
	e.recognizer.Decode(stream)
	return stream.GetResult().Text, nil
}
