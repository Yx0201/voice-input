package asr

// 云端流式引擎:百炼 Qwen-Audio-3.0-ASR-Flash-Streaming,WebSocket 协议。
// 协议(2026-09-19 官方文档核实):
//   wss://dashscope.aliyuncs.com/api-ws/v1/inference(或 {ws}.{region}.maas 专属域名)
//   握手头 Authorization: Bearer <key>
//   run-task{header{action,task_id,streaming:duplex},payload{task_group:audio,task:asr,
//            function:recognition,model,parameters{format:pcm,sample_rate:16000},input{}}}
//   → task-started 后持续发二进制 PCM16 帧
//   ← result-generated{payload.output.sentence{text,sentence_end,heartbeat,words...}}
//   finish-task → task-finished;失败为 task-failed{header.error_code/error_message}
// 服务端自带断句(sentence_end=true 即一句定稿),一个任务可跨多句。

import (
	cryptorand "crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"voice_input/internal/config"
)

// CloudStreamingEngine 百炼流式引擎;会话 = 一条 WebSocket 任务。
type CloudStreamingEngine struct {
	cfg config.Config
}

// NewCloudStreamingEngine 构造云端流式引擎(密钥可来自 config/env/钥匙串注入 cfg)。
func NewCloudStreamingEngine(cfg config.Config) (*CloudStreamingEngine, error) {
	if cfg.DashScopeAPIKey == "" {
		return nil, fmt.Errorf("云端流式需要 API Key(菜单「切换引擎→云端」输入,或设 DASHSCOPE_API_KEY)")
	}
	return &CloudStreamingEngine{cfg: cfg}, nil
}

// Name 实现 StreamingEngine。
func (e *CloudStreamingEngine) Name() string {
	return "cloud-streaming(" + streamingModelName(e.cfg.DashScopeModel) + ")"
}

// streamingModelName 从整句模型名推导流式变体(qwen-audio-3.0-asr-flash → …-flash-streaming)。
func streamingModelName(base string) string {
	if strings.HasSuffix(base, "-streaming") {
		return base
	}
	return base + "-streaming"
}

func (e *CloudStreamingEngine) endpoint() string {
	if ws := e.cfg.DashScopeWorkspaceID; ws != "" {
		return fmt.Sprintf("wss://%s.%s.maas.aliyuncs.com/api-ws/v1/inference", ws, e.cfg.Region)
	}
	return "wss://dashscope.aliyuncs.com/api-ws/v1/inference"
}

// NewSession 建立 WebSocket 连接并启动识别任务;阻塞至 task-started(或失败)。
func (e *CloudStreamingEngine) NewSession() (StreamingSession, error) {
	taskID := newTaskID()
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: http.ProxyFromEnvironment}
	conn, resp, err := dialer.Dial(e.endpoint(), http.Header{
		"Authorization": {"Bearer " + e.cfg.DashScopeAPIKey},
	})
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("WebSocket 连接失败(HTTP %d): %w", resp.StatusCode, err)
		}
		return nil, fmt.Errorf("WebSocket 连接失败(检查网络): %w", err)
	}

	s := &cloudStreamSession{
		conn:   conn,
		taskID: taskID,
		ready:  make(chan error, 1),
		stop:   make(chan struct{}),
	}
	s.wg.Add(1)
	go s.readLoop()

	runTask := wsMessage{
		Header: wsHeader{Action: "run-task", TaskID: taskID, Streaming: "duplex"},
		Payload: wsPayload{
			TaskGroup: "audio", Task: "asr", Function: "recognition",
			Model: streamingModelName(e.cfg.DashScopeModel),
			Parameters: wsParams{Format: "pcm", SampleRate: 16000},
			Input:      wsInput{},
		},
	}
	if err := s.writeJSON(runTask); err != nil {
		s.shutdown()
		return nil, fmt.Errorf("发送 run-task 失败: %w", err)
	}

	select {
	case err := <-s.ready:
		if err != nil {
			s.shutdown()
			return nil, err
		}
	case <-time.After(10 * time.Second):
		s.shutdown()
		return nil, fmt.Errorf("等待 task-started 超时")
	}
	return s, nil
}

// ---- 协议结构 ----

type wsHeader struct {
	Action       string `json:"action,omitempty"`
	Event        string `json:"event,omitempty"`
	TaskID       string `json:"task_id"`
	Streaming    string `json:"streaming,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
}

type wsParams struct {
	Format     string `json:"format"`
	SampleRate int    `json:"sample_rate"`
}

type wsInput struct {
	Context []map[string]string `json:"context,omitempty"`
}

type wsPayload struct {
	TaskGroup  string    `json:"task_group"`
	Task       string    `json:"task"`
	Function   string    `json:"function"`
	Model      string    `json:"model"`
	Parameters wsParams  `json:"parameters"`
	Input      wsInput   `json:"input"`
}

type wsMessage struct {
	Header  wsHeader  `json:"header"`
	Payload wsPayload `json:"payload"`
}

type wsSentence struct {
	Text         string `json:"text"`
	SentenceEnd  bool   `json:"sentence_end"`
	Heartbeat    bool   `json:"heartbeat"`
}

type wsServerMessage struct {
	Header struct {
		Event        string `json:"event"`
		TaskID       string `json:"task_id"`
		ErrorCode    string `json:"error_code"`
		ErrorMessage string `json:"error_message"`
	} `json:"header"`
	Payload struct {
		Output struct {
			Sentence wsSentence `json:"sentence"`
		} `json:"output"`
	} `json:"payload"`
}

// ---- 会话实现 ----

type cloudStreamSession struct {
	conn   *websocket.Conn
	taskID string

	writeMu     sync.Mutex // 串行化 WebSocket 写(音频帧与控制消息);finished 也由它保护
	finished    bool       // finish-task 是否已发出(FinishInput 提前发,Close 不重复)
	mu          sync.Mutex // 保护以下字段
	partialText string
	finalText   string
	endpoint    bool
	lastErr     error

	ready chan error
	stop  chan struct{}
	wg    sync.WaitGroup
}

// Feed 推送增量音频(PCM16LE 二进制帧)。
func (s *cloudStreamSession) Feed(pcm []float32, sampleRate int) {
	buf := make([]byte, len(pcm)*2)
	for i, v := range pcm {
		if v > 1 {
			v = 1
		} else if v < -1 {
			v = -1
		}
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(int16(v*32767)))
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.conn.WriteMessage(websocket.BinaryMessage, buf)
}

// FinishInput 云端实现:立即发 finish-task,让服务端冲刷并定稿最后一句。
// 不发的话服务端等不到更多音频也不定稿,收尾只能干等超时,尾句被吞。
func (s *cloudStreamSession) FinishInput() {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.finished {
		return
	}
	s.finished = true
	_ = s.conn.WriteJSON(wsMessage{
		Header:  wsHeader{Action: "finish-task", TaskID: s.taskID, Streaming: "duplex"},
		Payload: wsPayload{Input: wsInput{}},
	})
}

// PartialReliable 云端 partial 不可靠:服务端会回改已出现过的文字
// (实测"刘氏"→"楼市"级别的同音改写),激进预览提交会被回写卡死。
func (s *cloudStreamSession) PartialReliable() bool { return false }

// Partial 当前句累积文本(sentence_end 后自动清零,归属下一句)。
func (s *cloudStreamSession) Partial() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.partialText
}

// Endpoint 服务端是否已定稿一句。
func (s *cloudStreamSession) Endpoint() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.endpoint
}

// Finalize 取最近定稿句文本并复位;同一任务继续听下一句。
func (s *cloudStreamSession) Finalize() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.finalText
	s.finalText = ""
	s.endpoint = false
	s.partialText = ""
	return t
}

// Close 确保任务结束(finish-task 已在 FinishInput 发过则不重发)并关闭连接。
func (s *cloudStreamSession) Close() error {
	s.FinishInput()
	s.shutdown()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

func (s *cloudStreamSession) writeJSON(m wsMessage) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.conn.WriteJSON(m)
}

// readLoop 独立 goroutine 消费服务端事件。
func (s *cloudStreamSession) readLoop() {
	defer s.wg.Done()
	for {
		_, raw, err := s.conn.ReadMessage()
		if err != nil {
			select {
			case s.ready <- fmt.Errorf("连接中断: %w", err):
			default:
			}
			s.mu.Lock()
			s.lastErr = err
			// 连接已断不会再有结果:未定稿的 partial 升级为最终文本,
			// 并置 endpoint 让收尾轮询立即退出(网络中断时胶囊不空等超时)。
			if s.partialText != "" {
				s.finalText = s.partialText
				s.partialText = ""
			}
			s.endpoint = true
			s.mu.Unlock()
			return
		}
		var msg wsServerMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		switch msg.Header.Event {
		case "task-started":
			select {
			case s.ready <- nil:
			default:
			}
		case "result-generated":
			sen := msg.Payload.Output.Sentence
			if sen.Heartbeat {
				continue
			}
			s.mu.Lock()
			if sen.SentenceEnd {
				s.finalText = sen.Text
				s.endpoint = true
				s.partialText = ""
			} else {
				s.partialText = sen.Text
			}
			s.mu.Unlock()
		case "task-finished":
			// 任务结束不会再有结果:置 endpoint 让收尾轮询立即退出,
			// 而不是干等 8s 超时(如纯静默松手、无尾句可定稿的场景)。
			s.mu.Lock()
			s.endpoint = true
			s.mu.Unlock()
			return
		case "task-failed":
			err := fmt.Errorf("百炼任务失败 %s: %s", msg.Header.ErrorCode, msg.Header.ErrorMessage)
			select {
			case s.ready <- err:
			default:
			}
			s.mu.Lock()
			s.lastErr = err
			s.mu.Unlock()
			return
		}
	}
}

func (s *cloudStreamSession) shutdown() {
	select {
	case <-s.stop:
		return
	default:
		close(s.stop)
	}
	_ = s.conn.Close()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
}

// newTaskID 生成协议要求的 task_id(32 位十六进制 UUID 形态)。
func newTaskID() string {
	b := make([]byte, 16)
	_, _ = cryptorand.Read(b)
	return hex.EncodeToString(b)
}
