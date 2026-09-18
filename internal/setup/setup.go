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
	senseRepo = "csukuangfj/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17"
	vadRepo   = "csukuangfj/vad"
)

type fileSpec struct {
	url      string // 相对仓库的完整路径(repo/resolve/main/name)
	dest     string // 本地绝对路径
	minBytes int64  // 低于此大小视为不完整
}

// Progress 回调:当前文件名、已完成字节、总字节(未知为 -1)。
type Progress func(file string, done, total int64)

// EnsureModels 检查模型文件,缺失的自动下载。全部就绪返回 nil。
func EnsureModels(cfg config.Config, progress Progress) error {
	specs := []fileSpec{
		{fmt.Sprintf("%s/resolve/main/model.int8.onnx", senseRepo),
			filepath.Join(cfg.ModelDir, "model.int8.onnx"), 150_000_000},
		{fmt.Sprintf("%s/resolve/main/tokens.txt", senseRepo),
			filepath.Join(cfg.ModelDir, "tokens.txt"), 100_000},
		{fmt.Sprintf("%s/resolve/main/silero_vad.onnx", vadRepo),
			filepath.Join(cfg.VADDir, "silero_vad.onnx"), 1_500_000},
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
	tmp := s.dest + ".part"

	var lastErr error
	for _, host := range hosts {
		err := func() error {
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
					return rerr
				}
			}

			fi, err := os.Stat(tmp)
			if err != nil || fi.Size() < s.minBytes {
				return fmt.Errorf("文件不完整(%d 字节 < %d)", fi.Size(), s.minBytes)
			}
			if progress != nil {
				progress(filepath.Base(s.dest), done, done)
			}
			return os.Rename(tmp, s.dest)
		}()
		if err == nil {
			return nil
		}
		lastErr = err
		os.Remove(tmp)
	}
	return lastErr
}
