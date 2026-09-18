package asr

import (
	"fmt"
	"os"

	"github.com/go-audio/wav"
)

// ReadWavFile 读取 WAV 文件为 float32 PCM(±1.0)及采样率。
// 多声道输入自动取首声道降混(sherpa 模型要求单声道)。
// 用于里程碑 1 的文件级识别验证(不经过麦克风)。
func ReadWavFile(path string) ([]float32, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("打开 WAV 失败: %w", err)
	}
	defer f.Close()

	dec := wav.NewDecoder(f)
	if !dec.IsValidFile() {
		return nil, 0, fmt.Errorf("%s 不是有效的 WAV 文件(验证阶段请用 WAV 格式)", path)
	}

	buf, err := dec.FullPCMBuffer()
	if err != nil {
		return nil, 0, fmt.Errorf("解码 WAV 失败: %w", err)
	}
	if buf == nil || len(buf.Data) == 0 {
		return nil, 0, fmt.Errorf("%s 没有可解码的 PCM 数据", path)
	}

	sampleRate := int(dec.SampleRate)
	chans := int(dec.NumChans)
	data := buf.AsFloat32Buffer().Data

	if chans <= 1 {
		return data, sampleRate, nil
	}
	mono := make([]float32, 0, len(data)/chans)
	for i := 0; i+chans <= len(data); i += chans {
		mono = append(mono, data[i])
	}
	return mono, sampleRate, nil
}
