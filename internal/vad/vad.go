// Package vad 用 sherpa-onnx 内置的 silero VAD 做语音活动检测:
// 持续喂入 PCM,说完一句(静音超过阈值)自动切出一段完整语音。
package vad

import (
	"errors"
	"os"
	"path/filepath"

	sherpa_onnx "github.com/k2-fsa/sherpa-onnx-go-macos"

	"voice_input/internal/config"
)

// Detector 封装 VAD;Feed 持续喂音频,返回切分完整的语音段。
// 注意:不使用 sherpa 的 Clear()(实测会破坏内部状态导致后续永不出段),
// 需要清状态时整体重建 VAD 实例。
type Detector struct {
	vad       *sherpa_onnx.VoiceActivityDetector
	window    int       // 每次喂给 VAD 的采样数(silero 要求按窗口喂)
	pending   []float32 // 不足一个窗口的残余
	modelPath string
}

// New 创建 VAD,模型路径默认取 ~/.voice_input/models/vad/silero_vad.onnx。
func New(cfg config.Config) (*Detector, error) {
	modelPath := filepath.Join(cfg.VADDir, "silero_vad.onnx")
	if _, err := os.Stat(modelPath); err != nil {
		return nil, errors.New("VAD 模型不存在: " + modelPath + "(先运行 make setup)")
	}
	d := &Detector{window: 512, modelPath: modelPath}
	if err := d.create(); err != nil {
		return nil, err
	}
	return d, nil
}

// create (重新)构造底层 VAD 实例。
func (d *Detector) create() error {
	vadCfg := sherpa_onnx.VadModelConfig{
		SileroVad: sherpa_onnx.SileroVadModelConfig{
			Model:              d.modelPath,
			Threshold:          0.5,  // 语音概率阈值
			MinSilenceDuration: 0.55, // 静音多久算一句话说完(秒)
			MinSpeechDuration:  0.25, // 短于此的语音段丢弃(秒)
			MaxSpeechDuration:  10,   // 单句最长强制切分(秒)
			WindowSize:         512,  // silero @16k 标准窗口
		},
		SampleRate: 16000,
		NumThreads: 1,
		Provider:   "cpu",
		Debug:      0,
	}
	v := sherpa_onnx.NewVoiceActivityDetector(&vadCfg, 60) // 内部音频缓冲 60s
	if v == nil {
		return errors.New("加载 silero VAD 模型失败")
	}
	d.vad = v
	return nil
}

// Feed 喂入任意长度 PCM,返回本次切分完成的所有语音段(可能为空)。
func (d *Detector) Feed(pcm []float32) [][]float32 {
	d.pending = append(d.pending, pcm...)

	var segments [][]float32
	for len(d.pending) >= d.window {
		w := d.pending[:d.window]
		d.pending = d.pending[d.window:]

		d.vad.AcceptWaveform(w)
		for !d.vad.IsEmpty() {
			seg := d.vad.Front()
			d.vad.Pop()
			if seg != nil && len(seg.Samples) > 0 {
				segments = append(segments, seg.Samples)
			}
		}
	}
	return segments
}

// InSpeech 当前是否检测到说话中(用于状态展示)。
func (d *Detector) InSpeech() bool { return d.vad.IsSpeech() }

// Reset 丢弃未完结的语音状态(暂停听写时调用,避免恢复后吐出旧半句)。
// 整体重建底层实例,不依赖 Clear()。
func (d *Detector) Reset() {
	sherpa_onnx.DeleteVoiceActivityDetector(d.vad)
	d.pending = nil
	if err := d.create(); err != nil {
		// 重建失败属于异常情况(模型文件被删);保留 nil 由后续调用暴露
		d.vad = nil
	}
}

// Flush 强制收割未完结的语音段(按住说话松开时调用):
// 补喂约 0.8s 静音触发端点,返回切出的段落并重置状态。
func (d *Detector) Flush() [][]float32 {
	if d.vad == nil {
		return nil
	}
	silence := make([]float32, d.window*25) // 512 采样/窗 × 25 ≈ 0.8s
	var out [][]float32
	for i := 0; i < 2; i++ { // 最多补 ~1.6s 静音
		out = append(out, d.Feed(silence)...)
		if len(out) > 0 {
			break
		}
	}
	d.Reset()
	return out
}
