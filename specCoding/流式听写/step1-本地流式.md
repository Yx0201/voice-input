# step1 · 本地流式引擎(边说边出字)

> 状态:**草案**(2026-09-19) | 依赖:无 | 预计规模:internal/asr + cmd/voice-input 改造

## 现状盘点(实施起点)

- audioLoop 消费 mic→channel,经 silero VAD Feed 切段后调用 `Engine.Transcribe` 整句识别(见 ARCHITECTURE.md 听写管线)。
- sherpa-onnx 平台模块内 `OnlineRecognizer` 全套流式 API 可用;模型未下载。
- 菜单已有「切换引擎」子菜单模式可参照;config 已有 PatchConfig 持久化机制。

## 模型

- 主选:`csukuangfj/sherpa-onnx-streaming-paraformer-bilingual-zh-en`(中英,阿里 FunASR 系;含 encoder/decoder ONNX + tokens.txt)
- 备选(更轻):`csukuangfj/sherpa-onnx-streaming-zipformer-small-bilingual-zh-en-2023-02-16`
- 下载纳入既有三通道:make setup / 应用内下载器(setup.EnsureModels 增加文件条目)/ voice-input setup;hf-mirror 优先。

## 接口设计(internal/asr 新增)

```go
// StreamingSession 一个说话会话:边喂音频边取累积文本,端点后定稿。
type StreamingSession interface {
    Feed(pcm []float32, sampleRate int) // 喂入增量音频(内部按需 Decode)
    Partial() string                    // 当前累积识别文本
    Endpoint() bool                     // 是否检测到说完(模型自带 IsEndpoint)
    Finalize() string                   // 定稿并重置会话(开新会话由引擎负责)
}

// StreamingEngine 流式引擎(OnlineRecognizer 常驻,会话按句创建)。
type StreamingEngine interface {
    Name() string
    NewSession() StreamingSession
}
```

实现要点:OnlineRecognizerConfig 填 Paraformer 流式槽位;`recognizer.IsReady(stream)` 为真才 Decode;结果缓存供 Partial;Endpoint 触发后 Finalize 返回全文并 Reset。

## 核心循环改造(cmd/voice-input)

- 新增 dictation_mode(sentence/streaming),菜单「听写模式」子菜单(与「切换引擎」平级,AddSubMenuItemCheckbox 模式)。
- streaming 模式:audioLoop 把音频直接喂 session(不经 silero);每次 Feed 后执行「稳定前缀提交」;Endpoint → Finalize 补打 + NewSession。
- 模式与引擎正交;切换即时生效(参照现有 switchEngine 热切换)。

## 稳定前缀提交(打进第三方输入框的核心策略)

维护 `committed`(已注入文本)。每次 Partial() 后计算稳定部分 `stable`:

1. 最后一个句读标点(。?!;;、,)之前(含标点)的前缀视为稳定;
2. 兜底 A:连续 2 次 Partial 的公共前缀比 committed 多 ≥2 字 → 视为稳定;
3. 兜底 B:稳定部分超 6 字未提交 → 提交(防无标点长句迟迟不出字)。

增量 = stable 去掉 committed 前缀 → inject.TypeText,更新 committed。**永不发送退格**。

## 验收标准

1. 流式模式:说"今天天气真好",说到一半输入框已出现前几字,停顿后全句定稿、无重复无丢字;
2. 模式切换即时生效不重启;sentence 模式回归不受影响(现有 listen/app 行为不变);
3. 长句(15s+)、中英混说、噪声下稳定前缀不乱序不重复;
4. 定稿打一行日志(句长/耗时/文本),Partial 变化不打印。

## 实施记录

- (待开工)
