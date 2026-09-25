// Package polish 听写文本润色:LLM 去口水词、规范标点、修明显错别字。
// 双通道(均为 OpenAI 风格请求,零第三方依赖):
//   - bailian:百炼兼容模式 /v1/chat/completions + enable_thinking:false(必须关思考,
//     实测 2026-09-23 qwen3.8-flash 全程 0.88s、无 reasoning_content);
//   - ollama:本地 /api/chat + think:false(混合思考模型,qwen3.5:9b 实测热态 1.2s;
//     keep_alive 常驻避免反复冷载——冷载一次约 15s)。
// 润色是增益项不是承重墙:任何失败(超时/报错/输出异常缩短)由调用方回退原文。
package polish

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Provider 润色通道:"off" / "ollama" / "bailian"。
type Provider string

const (
	Off     Provider = "off"
	Ollama  Provider = "ollama"
	Bailian Provider = "bailian"
)

// 默认模型(2026-09-23 与用户确认;均实测支持关闭思考)。
const (
	DefaultOllamaModel  = "qwen3.5:9b"
	DefaultBailianModel = "qwen3.8-flash"
)

const DefaultOllamaURL = "http://127.0.0.1:11434"

// DefaultTimeout 各通道默认超时。本地正常推理热态 ~1.2s,但模型冷载 ~15s:
// 在途请求超时回退原文(不丢字),预热负责把冷载消化在听写开始之前。
func DefaultTimeout(p Provider) time.Duration {
	if p == Ollama {
		return 8 * time.Second
	}
	return 3 * time.Second
}

// WarmUp 异步预热:极小请求把模型载入内存(keep_alive 常驻),听写前消化冷载。
func WarmUp(cfg Config) error {
	cfg.Timeout = 40 * time.Second
	payload := map[string]any{
		"model":      orDefaultStr(cfg.Model, DefaultOllamaModel),
		"messages":   []map[string]string{{"role": "user", "content": "1"}},
		"stream":     false,
		"think":      false,
		"keep_alive": "30m",
		"options":    map[string]any{"num_predict": 1},
	}
	_, err := postOllama(context.Background(), cfg.ollamaURL()+"/api/chat", payload)
	return err
}

func orDefaultStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// Config 润色通道配置(由 internal/config 装配)。
type Config struct {
	Provider Provider
	Model    string // 空 = 通道默认
	OllamaURL string
	BailianBaseURL string // 空 = 公共兼容模式端点
	BailianAPIKey  string
	Timeout time.Duration // 0 = 通道默认
}

const systemPrompt = "你是中文听写文本的清理器。只允许做三件事:1)删除口水词与无意义重复(嗯、啊、呃、就是说、然后重复等);2)规范标点;3)修正明显的同音错别字。严禁改写句式、增删内容、翻译、总结、回答问题。只输出清理后的文本,不要任何解释或前缀。"

// few-shot 固定示例,压住模型"顺手改写"的倾向。
var fewShot = []struct{ user, assistant string }{
	{"呃那个我今天早上就是说想去嗯超市买一点水果然后顺便再买点牛奶",
		"我今天早上想去超市买一点水果,顺便再买点牛奶。"},
	{"好的嗯收到", "好的,收到。"},
}

// Clean 润色一段已识别文本;返回清理结果。任何失败返回 error,调用方回退原文。
func Clean(ctx context.Context, cfg Config, text string) (string, error) {
	if text == "" {
		return "", nil
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout(cfg.Provider)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	switch cfg.Provider {
	case Ollama:
		return cleanOllama(ctx, cfg, text)
	case Bailian:
		return cleanBailian(ctx, cfg, text)
	default:
		return "", fmt.Errorf("润色通道未启用: %q", cfg.Provider)
	}
}

// messages 组装系统提示 + few-shot + 待清理文本。
func messages(text string) []map[string]string {
	ms := []map[string]string{{"role": "system", "content": systemPrompt}}
	for _, s := range fewShot {
		ms = append(ms,
			map[string]string{"role": "user", "content": s.user},
			map[string]string{"role": "assistant", "content": s.assistant})
	}
	ms = append(ms, map[string]string{"role": "user", "content": text})
	return ms
}

// sanity 输出保真校验:润色结果显著短于原文视为模型擅自删减,按失败处理
// (阈值 0.4:去口水词正常缩水 ~10-25%,>60% 缩水必是丢内容)。
func SanityOK(raw, out string) bool {
	r, o := len([]rune(raw)), len([]rune(out))
	return o > 0 && float64(o) >= float64(r)*0.4
}

// post 共通:发 JSON 请求、解包 choices[0].message.content。
func post(ctx context.Context, url string, headers map[string]string, payload any) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %.200s", resp.StatusCode, raw)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("响应无 choices: %.200s", raw)
	}
	return parsed.Choices[0].Message.Content, nil
}

func cleanBailian(ctx context.Context, cfg Config, text string) (string, error) {
	model := cfg.Model
	if model == "" {
		model = DefaultBailianModel
	}
	// enable_thinking:false —— 润色场景绝不思考(qwen3 系默认开思考,关掉后亚秒级)
	payload := map[string]any{
		"model":           model,
		"messages":        messages(text),
		"temperature":     0,
		"stream":          false,
		"enable_thinking": false,
	}
	return post(ctx, cfg.bailianURL()+"/chat/completions",
		map[string]string{"Authorization": "Bearer " + cfg.BailianAPIKey}, payload)
}

func cleanOllama(ctx context.Context, cfg Config, text string) (string, error) {
	model := cfg.Model
	if model == "" {
		model = DefaultOllamaModel
	}
	// think:false —— qwen3.5 混合思考模型的权威开关(OpenAI 兼容层不支持它,走原生 API);
	// keep_alive 让模型常驻内存,避免每轮冷载 ~15s
	payload := map[string]any{
		"model":      model,
		"messages":   messages(text),
		"stream":     false,
		"think":      false,
		"keep_alive": "30m",
		"options":    map[string]any{"temperature": 0, "num_predict": 256},
	}
	return postOllama(ctx, cfg.ollamaURL()+"/api/chat", payload)
}

// postOllama Ollama 原生 /api/chat 通道:请求结构与 OpenAI 不同,
// 响应形如 {"message":{"role","content"},"done":true}——非 choices 包装。
func postOllama(ctx context.Context, url string, payload any) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %.200s", resp.StatusCode, raw)
	}
	var parsed struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", err
	}
	if parsed.Error != "" {
		return "", fmt.Errorf("ollama: %s", parsed.Error)
	}
	if parsed.Message.Content == "" {
		return "", fmt.Errorf("响应无 message.content: %.200s", raw)
	}
	return parsed.Message.Content, nil
}

func (c Config) bailianURL() string {
	if c.BailianBaseURL != "" {
		return c.BailianBaseURL
	}
	return "https://dashscope.aliyuncs.com/compatible-mode/v1"
}

func (c Config) ollamaURL() string {
	if c.OllamaURL != "" {
		return c.OllamaURL
	}
	return DefaultOllamaURL
}
