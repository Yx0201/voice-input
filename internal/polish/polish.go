// Package polish 听写文本润色:云端 LLM(qwen3.8-flash)去口水词、规范标点、
// 修明显错别字。仅云端单通道(2026-09-25 用户裁决:本地 Ollama 慢且多一套预热,
// 收敛到复用云端引擎已有的 API Key;OpenAI 兼容模式,零第三方依赖)。
// 定位是增益不是承重:可用性由调用方判定(无 Key 静默失效),请求失败一律
// 回退原文——润色永不阻塞、永不丢字。
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

// DefaultBailianModel 默认润色模型(2026-09-23 实测:enable_thinking:false 下
// 全程 0.88s,无思考内容)。
const DefaultBailianModel = "qwen3.8-flash"

const DefaultBailianBaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"

// DefaultTimeout 单次润色超时;超时即回退原文,听写节奏不受影响。
const DefaultTimeout = 3 * time.Second

// Config 润色通道配置(由 internal/config 装配)。
type Config struct {
	Model          string // 空 = DefaultBailianModel
	BailianBaseURL string // 空 = 公共兼容模式端点
	BailianAPIKey  string
	Timeout        time.Duration // 0 = DefaultTimeout
	TargetLang     string        // 翻译目标语言:""=只润色(中文);"en"=清理并翻译成英文
}

const systemPrompt = "你是中文听写文本的清理器。只允许做三件事:1)删除口水词与无意义重复(嗯、啊、呃、就是说、然后重复等);2)规范标点;3)修正明显的同音错别字。严禁改写句式、增删内容、翻译、总结、回答问题。只输出清理后的文本,不要任何解释或前缀。"

const translatePrompt = "你是中文语音听写的翻译器。把用户的中文口语转成自然流畅的英文:1)先在心里去掉口水词与无意义重复(嗯、啊、呃、就是说);2)修正明显的同音错别字;3)翻译成自然地道的英文书面语,不要逐字直译;4)保留专有名词、数字与代码术语。只输出英文译文,不要任何解释、原文或前缀。"

// few-shot 固定示例,压住模型"顺手改写/自由发挥"的倾向(按模式选用)。
var fewShot = []struct{ user, assistant string }{
	{"呃那个我今天早上就是说想去嗯超市买一点水果然后顺便再买点牛奶",
		"我今天早上想去超市买一点水果,顺便再买点牛奶。"},
	{"好的嗯收到", "好的,收到。"},
}

var translateFewShot = []struct{ user, assistant string }{
	{"呃那个我今天早上就是说想去嗯超市买一点水果然后顺便再买点牛奶",
		"I'm going to the supermarket this morning to buy some fruit, and pick up some milk as well."},
	{"好的嗯收到", "Got it."},
}

// Translating 是否处于翻译输出模式。
func (c Config) Translating() bool { return c.TargetLang != "" }

// Clean 润色一段已识别文本;返回清理结果。任何失败返回 error,调用方回退原文。
func Clean(ctx context.Context, cfg Config, text string) (string, error) {
	if text == "" {
		return "", nil
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	model := cfg.Model
	if model == "" {
		model = DefaultBailianModel
	}
	// enable_thinking:false —— 润色场景绝不思考(qwen3 系默认开思考,关掉后亚秒级)
	payload := map[string]any{
		"model":           model,
		"messages":        messages(cfg, text),
		"temperature":     0,
		"stream":          false,
		"enable_thinking": false,
	}
	return post(ctx, cfg.baseURL()+"/chat/completions",
		map[string]string{"Authorization": "Bearer " + cfg.BailianAPIKey}, payload)
}

// SanityOK 输出保真校验:润色结果显著短于原文视为模型擅自删减,按失败处理
// (阈值 0.4:去口水词正常缩水 ~10-25%,>60% 缩水必是丢内容)。
// 翻译模式下不适用(中英字数不可比),由调用方跳过。
func SanityOK(raw, out string) bool {
	r, o := len([]rune(raw)), len([]rune(out))
	return o > 0 && float64(o) >= float64(r)*0.4
}

// messages 组装系统提示 + few-shot + 待处理文本(按翻译模式选提示与示例)。
func messages(cfg Config, text string) []map[string]string {
	prompt := systemPrompt
	shots := fewShot
	if cfg.Translating() {
		prompt = translatePrompt
		shots = translateFewShot
	}
	ms := []map[string]string{{"role": "system", "content": prompt}}
	for _, s := range shots {
		ms = append(ms,
			map[string]string{"role": "user", "content": s.user},
			map[string]string{"role": "assistant", "content": s.assistant})
	}
	ms = append(ms, map[string]string{"role": "user", "content": text})
	return ms
}

// post 发 JSON 请求、解包 choices[0].message.content。
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

func (c Config) baseURL() string {
	if c.BailianBaseURL != "" {
		return c.BailianBaseURL
	}
	return DefaultBailianBaseURL
}
