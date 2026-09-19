// Package setup 负责模型文件的检查与按需下载(应用内置,取代外部脚本)。
// 下载源优先 hf-mirror.com(国内直连可用),失败回退 huggingface.co。
package setup

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"voice_input/internal/config"
)

const (
	senseRepo    = "csukuangfj/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17"
	streamRepo   = "csukuangfj/sherpa-onnx-streaming-paraformer-bilingual-zh-en"
	vadRepo      = "csukuangfj/vad"
	streamingDir = "streaming-paraformer"
)

type fileSpec struct {
	url      string // 相对仓库的完整路径(repo/resolve/main/name)
	dest     string // 本地绝对路径
	minBytes int64  // 低于此大小视为不完整
}

// Progress 回调:当前文件名、已完成字节、总字节(未知为 -1)。
type Progress func(file string, done, total int64)

// ModelsReady 本地识别模型是否齐备(SenseVoice + VAD)。
func ModelsReady(cfg config.Config) bool {
	specs := []struct {
		dest     string
		minBytes int64
	}{
		{filepath.Join(cfg.ModelDir, "model.int8.onnx"), 150_000_000},
		{filepath.Join(cfg.ModelDir, "tokens.txt"), 100_000},
		{filepath.Join(cfg.VADDir, "silero_vad.onnx"), 1_500_000},
	}
	for _, s := range specs {
		if fi, err := os.Stat(s.dest); err != nil || fi.Size() < s.minBytes {
			return false
		}
	}
	return true
}

// StreamingModelsReady 本地流式模型(streaming-paraformer)是否齐备。
func StreamingModelsReady() bool {
	dir := filepath.Join(config.DefaultDir(), "models", streamingDir)
	specs := []struct {
		name     string
		minBytes int64
	}{
		{"encoder.int8.onnx", 50_000_000},
		{"decoder.int8.onnx", 500_000},
		{"tokens.txt", 50_000},
	}
	for _, s := range specs {
		fi, err := os.Stat(filepath.Join(dir, s.name))
		if err != nil || fi.Size() < s.minBytes {
			return false
		}
	}
	return true
}

// EnsureVAD 只下载缺失的 VAD 断句模型(1.7MB)——云端引擎用户无需下载 228MB 识别模型。
func EnsureVAD(cfg config.Config, progress Progress) error {
	dest := filepath.Join(cfg.VADDir, "silero_vad.onnx")
	if fi, err := os.Stat(dest); err == nil && fi.Size() >= 1_500_000 {
		return nil
	}
	return download([]string{"https://hf-mirror.com", "https://huggingface.co"},
		fileSpec{fmt.Sprintf("%s/resolve/main/silero_vad.onnx", vadRepo), dest, 1_500_000}, progress)
}

// EnsureModels 检查模型文件,缺失的自动下载。全部就绪返回 nil。
// 含:SenseVoice 整句引擎、silero VAD、streaming-paraformer 流式引擎。
func EnsureModels(cfg config.Config, progress Progress) error {
	streamDir := filepath.Join(config.DefaultDir(), "models", streamingDir)
	specs := []fileSpec{
		{fmt.Sprintf("%s/resolve/main/model.int8.onnx", senseRepo),
			filepath.Join(cfg.ModelDir, "model.int8.onnx"), 150_000_000},
		{fmt.Sprintf("%s/resolve/main/tokens.txt", senseRepo),
			filepath.Join(cfg.ModelDir, "tokens.txt"), 100_000},
		{fmt.Sprintf("%s/resolve/main/silero_vad.onnx", vadRepo),
			filepath.Join(cfg.VADDir, "silero_vad.onnx"), 1_500_000},
		{fmt.Sprintf("%s/resolve/main/encoder.int8.onnx", streamRepo),
			filepath.Join(streamDir, "encoder.int8.onnx"), 50_000_000},
		{fmt.Sprintf("%s/resolve/main/decoder.int8.onnx", streamRepo),
			filepath.Join(streamDir, "decoder.int8.onnx"), 500_000},
		{fmt.Sprintf("%s/resolve/main/tokens.txt", streamRepo),
			filepath.Join(streamDir, "tokens.txt"), 100_000},
	}

	var missing []fileSpec
	for _, s := range specs {
		if fi, err := os.Stat(s.dest); err != nil || fi.Size() < s.minBytes {
			missing = append(missing, s)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	hosts := []string{"https://hf-mirror.com", "https://huggingface.co"}
	for _, s := range missing {
		if err := download(hosts, s, progress); err != nil {
			return fmt.Errorf("下载 %s 失败: %w", filepath.Base(s.dest), err)
		}
	}
	return nil
}

func download(hosts []string, s fileSpec, progress Progress) error {
	if err := os.MkdirAll(filepath.Dir(s.dest), 0o755); err != nil {
		return err
	}

	var lastErr error
	for _, host := range hosts {
		// 每个源尝试 2 次(镜像对小文件偶发限流,稍候重试即过)
		for attempt := 0; attempt < 2; attempt++ {
			err := downloadOnce(host, s, progress)
			if err == nil {
				return nil
			}
			lastErr = err
			if attempt == 0 {
				time.Sleep(3 * time.Second)
			}
		}
	}
	return lastErr
}

func downloadOnce(host string, s fileSpec, progress Progress) error {
	tmp := s.dest + ".part"
	url := host + "/" + s.url
	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer f.Close()

	total := resp.ContentLength // 可能 -1(分块传输)
	var done int64
	buf := make([]byte, 64*1024)
	lastReport := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				os.Remove(tmp)
				return werr
			}
			done += int64(n)
			if progress != nil && time.Since(lastReport) > 800*time.Millisecond {
				progress(filepath.Base(s.dest), done, total)
				lastReport = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			os.Remove(tmp)
			return rerr
		}
	}

	fi, err := os.Stat(tmp)
	if err != nil || fi.Size() < s.minBytes {
		os.Remove(tmp)
		return fmt.Errorf("文件不完整(%d 字节 < %d)", fi.Size(), s.minBytes)
	}
	if progress != nil {
		progress(filepath.Base(s.dest), done, done)
	}
	return os.Rename(tmp, s.dest)
}
