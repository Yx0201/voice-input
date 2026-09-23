package asr

// 流式听写引擎(边说边出字):会话式接口,增量喂音频,
// 随时读当前累积文本,端点(说完一句)后定稿。
// 本地实现走 sherpa-onnx 的 OnlineRecognizer(streaming-paraformer)。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	sherpa_onnx "github.com/k2-fsa/sherpa-onnx-go-macos"

	"voice_input/internal/config"
)

// StreamingSession 一个说话会话。生命周期 = 一次连续听写(开始→暂停/退出)。
// 接口约定:Feed 只投递不阻塞消费方;Partial 返回当前句累积文本;
// Endpoint 为真后调用 Finalize 取定稿文本并继续同一会话;Close 结束会话。
// FinishInput 声明"不再有音频"(松手关麦后调用):本地实现补尾垫静音冲刷
// 解码缓冲,否则端点检测看不到尾随静音、Endpoint 永不触发。
// PartialReliable 报告 Partial 文本是否只增不改:本地 paraformatter 分段内
// append-only(可靠,可激进预览提交);云端 partial 会回改早前文字(不可靠,
// 只能按标点锚定做保守预览,否则已提交前缀被重写后打字守卫永久卡死)。
type StreamingSession interface {
	Feed(pcm []float32, sampleRate int)
	FinishInput()
	PartialReliable() bool
	Partial() string
	Endpoint() bool
	Finalize() string
	Close() error
}

// StreamingEngine 创建流式会话(本地/云端实现)。
type StreamingEngine interface {
	Name() string
	NewSession() (StreamingSession, error)
}

// ---- 本地流式(sherpa-onnx streaming-paraformer) ----

// LocalStreamingEngine 基于 streaming-paraformer-bilingual-zh-en 的本地流式引擎。
type LocalStreamingEngine struct {
	recognizer *sherpa_onnx.OnlineRecognizer
}

// StreamingModelDir 本地流式模型目录(与 sense-voice 同级)。
func StreamingModelDir() string {
	return filepath.Join(config.DefaultDir(), "models", "streaming-paraformer")
}

// NewLocalStreamingEngine 加载流式模型;文件缺失时返回带路径提示的错误。
func NewLocalStreamingEngine() (*LocalStreamingEngine, error) {
	dir := StreamingModelDir()
	encoder := filepath.Join(dir, "encoder.int8.onnx")
	decoder := filepath.Join(dir, "decoder.int8.onnx")
	tokens := filepath.Join(dir, "tokens.txt")
	for _, p := range []string{encoder, decoder, tokens} {
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("流式模型不存在: %s(菜单「下载缺失模型」或 voice-input setup)", p)
		}
	}

	conf := sherpa_onnx.OnlineRecognizerConfig{
		FeatConfig: sherpa_onnx.FeatureConfig{SampleRate: 16000, FeatureDim: 80},
		ModelConfig: sherpa_onnx.OnlineModelConfig{
			Paraformer: sherpa_onnx.OnlineParaformerModelConfig{
				Encoder: encoder,
				Decoder: decoder,
			},
			Tokens:     tokens,
			NumThreads: 2,
			Debug:      0,
			Provider:   "cpu",
		},
		DecodingMethod: "greedy_search",
		// 端点检测默认禁用,必须显式开启;阈值面向听写调优(说完约 0.8s 定稿)
		EnableEndpoint:          1,
		Rule1MinTrailingSilence: 0.8,
		Rule2MinTrailingSilence: 1.2,
		Rule3MinUtteranceLength: 20,
	}
	r := sherpa_onnx.NewOnlineRecognizer(&conf)
	if r == nil {
		return nil, errors.New("加载流式模型失败(sherpa-onnx 返回空识别器,检查模型文件完整性)")
	}
	return &LocalStreamingEngine{recognizer: r}, nil
}

// Name 实现 StreamingEngine。
func (e *LocalStreamingEngine) Name() string { return "local-streaming(paraformer)" }

// NewSession 实现 StreamingEngine。
func (e *LocalStreamingEngine) NewSession() (StreamingSession, error) {
	s := sherpa_onnx.NewOnlineStream(e.recognizer)
	if s == nil {
		return nil, errors.New("创建流式音频流失败")
	}
	return &localStreamSession{engine: e, stream: s}, nil
}

type localStreamSession struct {
	engine  *LocalStreamingEngine
	stream  *sherpa_onnx.OnlineStream
}

// Feed 喂增量音频并尽量解码(不阻塞过久:每次至多解码到不 IsReady)。
func (s *localStreamSession) Feed(pcm []float32, sampleRate int) {
	s.stream.AcceptWaveform(sampleRate, pcm)
	for s.engine.recognizer.IsReady(s.stream) {
		s.engine.recognizer.Decode(s.stream)
	}
}

// FinishInput 本地实现:显式补足静音并冲刷到定稿。静音量按最坏情况算:
// Rule1 要 0.8s 尾静音,而块解码会"吃掉"最多两块(chunk=61 帧≈0.61s×2——
// 尾字 token 的非空白帧位置记在发射时刻、不足一块的尾帧默认不解码),
// 因此喂 2.5s 保证端点必触发;is_final 让最后不足一块的帧也参与解码,
// 尾部文字不再滞留缓冲(v1.13.8 paraformer 支持的每流选项)。
func (s *localStreamSession) FinishInput() {
	s.stream.AcceptWaveform(16000, make([]float32, 40000)) // 2.5s 零值=静音
	s.stream.SetOption("is_final", "1")
	s.stream.InputFinished()
	for s.engine.recognizer.IsReady(s.stream) {
		s.engine.recognizer.Decode(s.stream)
	}
}

// PartialReliable 本地 partial 分段内 append-only(sherpa tokens 只增,
// 30s 连续语音 + 变长块 + 双会话探针验证无前缀回退)。
func (s *localStreamSession) PartialReliable() bool { return true }

// Partial 当前累积识别文本。
func (s *localStreamSession) Partial() string {
	return s.engine.recognizer.GetResult(s.stream).Text
}

// Endpoint 模型自带的端点检测(说完一句)。
func (s *localStreamSession) Endpoint() bool {
	return s.engine.recognizer.IsEndpoint(s.stream)
}

// Finalize 定稿当前句并重置端点状态,同一会话继续听下一句。
func (s *localStreamSession) Finalize() string {
	text := s.engine.recognizer.GetResult(s.stream).Text
	s.engine.recognizer.Reset(s.stream)
	return text
}

// Close 结束会话(本地无外部资源)。
func (s *localStreamSession) Close() error { return nil }

// commonPrefix 返回两个字符串的最长公共前缀(按字节,中文安全:
// UTF-8 前缀截断可能切在多字节字符中间,这里按 rune 处理)。
func commonPrefix(a, b string) string {
	ar, br := []rune(a), []rune(b)
	n := len(ar)
	if len(br) < n {
		n = len(br)
	}
	i := 0
	for i < n && ar[i] == br[i] {
		i++
	}
	return string(ar[:i])
}
