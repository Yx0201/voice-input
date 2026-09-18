package asr

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"voice_input/internal/config"
)

// CloudEngine 基于阿里百炼 qwen-audio-3.0-asr-flash 的云端引擎。
//
// 接口(2026-09 官方文档核实):
//   POST https://dashscope.aliyuncs.com/api/v1/services/aigc/multimodal-generation/generation
//   (配置了 workspace_id 时走 https://{ws}.{region}.maas.aliyuncs.com 同路径)
//   Authorization: Bearer <key>
//   body: {"model":"qwen-audio-3.0-asr-flash",
//          "input":{"messages":[{"role":"user","content":[
//              {"type":"input_audio","input_audio":{"data":"data:audio/wav;base64,..."}}]}]},
//          "parameters":{"format":"wav","sample_rate":"16000"}}
//   响应: output.text 为完整识别文本;base64 上限 10MB。
type CloudEngine struct {
	cfg    config.Config
	client *http.Client
}

// NewCloudEngine 构造云端引擎;密钥可来自 config(env/json)或钥匙串。
func NewCloudEngine(cfg config.Config) (*CloudEngine, error) {
	if cfg.DashScopeAPIKey == "" {
		return nil, fmt.Errorf("云端引擎需要 API Key(菜单切换时输入,或设 DASHSCOPE_API_KEY)")
	}
	return &CloudEngine{
		cfg:    cfg,
		client: &http.Client{Timeout: 60 * time.Second},
	}, nil
}

// Name 实现 Engine 接口。
func (e *CloudEngine) Name() string {
	return fmt.Sprintf("cloud(%s)", e.cfg.DashScopeModel)
}

// Transcribe 实现 Engine 接口:PCM → WAV → base64 → 百炼多模态接口。
func (e *CloudEngine) Transcribe(pcm []float32, sampleRate int) (string, error) {
	if len(pcm) == 0 {
		return "", nil
	}
	wav := PCMToWav(pcm, sampleRate)
	if len(wav) > 9*1024*1024 {
		return "", fmt.Errorf("音频过大(%.1fMB,云端 base64 上限 10MB)", float64(len(wav))/1e6)
	}

	req := cloudRequest{Model: e.cfg.DashScopeModel}
	req.Input.Messages = []cloudMessage{{
		Role: "user",
		Content: []cloudContent{{
			Type:       "input_audio",
			InputAudio: cloudInputAudio{Data: "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(wav)},
		}},
	}}
	req.Parameters.Format = "wav"
	req.Parameters.SampleRate = fmt.Sprintf("%d", sampleRate)

	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequest(http.MethodPost, e.endpoint(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+e.cfg.DashScopeAPIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("网络请求失败(检查网络): %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var cr cloudResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return "", fmt.Errorf("响应解析失败(HTTP %d): %.200s", resp.StatusCode, string(raw))
	}
	if cr.Code != "" || cr.Message != "" {
		return "", fmt.Errorf("百炼错误 %s: %s", cr.Code, cr.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %.200s", resp.StatusCode, string(raw))
	}
	return cr.Output.Text, nil
}

// endpoint 未配置 workspace 时用公共端点;配置后走业务空间专属端点。
func (e *CloudEngine) endpoint() string {
	if e.cfg.DashScopeWorkspaceID != "" {
		return fmt.Sprintf("https://%s.%s.maas.aliyuncs.com/api/v1/services/aigc/multimodal-generation/generation",
			e.cfg.DashScopeWorkspaceID, e.cfg.Region)
	}
	return "https://dashscope.aliyuncs.com/api/v1/services/aigc/multimodal-generation/generation"
}

// PCMToWav 把 float32 单声道 PCM 编码为 16-bit WAV 字节流。
func PCMToWav(pcm []float32, sampleRate int) []byte {
	data := make([]byte, len(pcm)*2)
	for i, s := range pcm {
		if s > 1 {
			s = 1
		} else if s < -1 {
			s = -1
		}
		binary.LittleEndian.PutUint16(data[i*2:], uint16(int16(s*32767)))
	}

	var hdr [44]byte
	copy(hdr[0:], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(36+len(data)))
	copy(hdr[8:], "WAVE")
	copy(hdr[12:], "fmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)       // fmt 块长度
	binary.LittleEndian.PutUint16(hdr[20:], 1)        // PCM
	binary.LittleEndian.PutUint16(hdr[22:], 1)        // 单声道
	binary.LittleEndian.PutUint32(hdr[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(hdr[28:], uint32(sampleRate)*2) // 字节率
	binary.LittleEndian.PutUint16(hdr[32:], 2)        // 块对齐
	binary.LittleEndian.PutUint16(hdr[34:], 16)       // 位深
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], uint32(len(data)))

	return append(hdr[:], data...)
}

// ---- 请求/响应结构 ----

type cloudRequest struct {
	Model      string         `json:"model"`
	Input      cloudInput     `json:"input"`
	Parameters cloudParams    `json:"parameters"`
}

type cloudInput struct {
	Messages []cloudMessage `json:"messages"`
}

type cloudMessage struct {
	Role    string         `json:"role"`
	Content []cloudContent `json:"content"`
}

type cloudContent struct {
	Type       string          `json:"type"`
	InputAudio cloudInputAudio `json:"input_audio"`
}

type cloudInputAudio struct {
	Data string `json:"data"`
}

type cloudParams struct {
	Format     string `json:"format"`
	SampleRate string `json:"sample_rate"`
}

type cloudResponse struct {
	Output struct {
		Text string `json:"text"`
	} `json:"output"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}
