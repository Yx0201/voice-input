# 流式听写(边说边出字)实施规格

> 目标:说话时文字近乎实时(落后语音约 0.3-0.5s)滚动打进焦点输入框,
> 与现有"说完出字"模式共存,菜单可切换。本 spec 定稿于 2026-09-19,分两期实施。

## 背景结论(已调研)

- 现有 SenseVoice 为离线模型,必须整句输入 → 靠 VAD 等静音切句,体验为"说完停顿才出字"。
- 流式模型按块(0.3-0.6s)持续解码,随时输出累积文本,即微信语音输入体验。
- **本地路线**:sherpa-onnx Go 绑定已内置全套流式 API(`OnlineRecognizer/NewOnlineStream/
  AcceptWaveform/Decode/GetResult/IsEndpoint/Reset`),与现有 Offline API 同库,零新依赖。
- **云端路线**:百炼 `Qwen-Audio-3.0-ASR-Flash-Streaming`,WebSocket 协议,音频流入文本流出,
  按输入音频秒计费;能力与已验收的非流式版同级(方言/行业词/中英混合强)。
- 关键差异点:微信画自己的输入框可任意重绘;我们向第三方 app 打字,需"稳定前缀提交"策略(见下)。

## v1:本地流式(先做)

### 模型

- 主选:`csukuangfj/sherpa-onnx-streaming-paraformer-bilingual-zh-en`(中英,阿里 FunASR 系)
- 备选(更轻):`csukuangfj/sherpa-onnx-streaming-zipformer-small-bilingual-zh-en`(2023-02-16 版)
- 下载走 hf-mirror,纳入 `make setup` / 应用内下载器 / `voice-input setup` 三条既有通道

### 接口设计(internal/asr)

```go
// StreamingSession 一个说话会话:边喂音频边取累积文本,端点后定稿。
type StreamingSession interface {
    Feed(pcm []float32, sampleRate int) // 喂入增量音频
    Partial() string                    // 当前累积识别文本
    Endpoint() bool                     // 是否检测到说完(IsEndpoint)
    Finalize() string                   // 定稿并重置会话
}

// 流式引擎构造会话(OnlineRecognizer 常驻,会话按句创建)。
type StreamingEngine interface {
    Name() string
    NewSession() StreamingSession
}
```

### 核心循环改造(app 模式)

- 流式模式下麦克风音频**直接喂 StreamingSession**(不再经 silero VAD;断句用模型自带 Endpoint)
- 每次 Feed 后取 `Partial()`,计算稳定前缀并增量注入(见下)
- `Endpoint()` 成立 → `Finalize()` 补打剩余文本,开新会话

### 稳定前缀提交(打进第三方输入框的核心策略)

- 维护 `committed`(已打进输入框的文本)
- `Partial()` 结果中,以下部分视为稳定:
  - 最后一个句读标点(。?!;;、,逗号)之前(含标点)的前缀
  - 兜底:若连续 2 次更新前缀一致且比 committed 多 ≥2 字,也视为稳定
  - 兜底2:稳定部分超 6 字未提交则提交(防长句无标点迟迟不出字)
- 增量 = stable 去掉 committed 前缀 → CGEvent 注入,更新 committed
- 永不回删(不发送退格),未稳定尾部等下次更新再补

### 菜单与配置

- 菜单新增「听写模式」子菜单(与「切换引擎」平级):
  - `完整句模式(更准)` = 现状(Offline+VAD)
  - `即时出字(流式)` = v1 新增
- 选择持久化 `config.json` → `"dictation_mode": "sentence" | "streaming"`
- 模式与引擎正交组合(本地流式 / 本地整句 / 云端整句;云端流式 v2)

### 日志

- 定稿时打一行(句长/耗时/文本),partial 变化不打印(防刷屏)

## v2:云端流式(后做)

- 模型:`qwen-audio-3.0-asr-flash-streaming`(WebSocket,百炼)
- 协议:连接 → run-task → 持续推音频帧 → 服务端流式返回(sentence.text 增量)→ finish-task
- Key 复用钥匙串现有条目;WebSocket 客户端用 `gorilla/websocket` 或 `nhooyr.io/websocket`
- 断线重连:指数退避,连续失败 3 次弹状态栏提示并降级当前引擎的非流式路径
- 成本提示:按输入秒计费,菜单 tooltip 注明

## 验收标准

1. 流式模式:说"今天天气真好",说到一半输入框已出现前几字,停顿后全句定稿、无重复无丢字
2. 模式切换即时生效,不重启应用;`sentence` 模式回归不受影响
3. 长句(15s+)、中英混说、噪音环境下稳定前缀策略不出现乱序/重复
4. README 与「使用帮助」文案同步更新

## 风险与预案

| 风险 | 预案 |
|---|---|
| paraformer 流式精度低于 SenseVoice 整句 | 模式默认仍为 sentence;流式定位为体验优先选项 |
| 稳定前缀在无标点长句迟迟不提交 | 已设 6 字兜底阈值,可配置 |
| 流式模型体积/下载失败 | 备选 zipformer-small;下载器已支持逐文件校验 |
| 云端流式长开费用 | 状态栏显示计费模式;文档明示按秒计费 |
