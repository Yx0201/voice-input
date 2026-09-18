# 流式听写(边说边出字)· 总览

> 状态:**草案**(2026-09-19 起草,待用户确认后开工)
> 目标:说话时文字近乎实时(落后语音约 0.3-0.5s)滚动打进焦点输入框,与现有"说完出字"模式共存,菜单可切换。

## 背景与结论(已调研)

- 现有 SenseVoice 为离线模型,必须整句输入 → 靠 VAD 等静音切句,体验为"说完停顿才出字"。
- 流式模型按块(0.3-0.6s)持续解码,随时输出累积文本,即微信语音输入体验。
- **本地路线**:sherpa-onnx Go 绑定已内置全套流式 API(`OnlineRecognizer/NewOnlineStream/AcceptWaveform/Decode/GetResult/IsEndpoint/Reset`),与现有 Offline API 同库,零新依赖。
- **云端路线**:百炼 `qwen-audio-3.0-asr-flash-streaming`,WebSocket 协议(音频流入文本流出),按输入音频秒计费;能力与已验收的非流式版同级。
- 关键差异:微信画自己的输入框可任意重绘;我们向第三方 app 打字,需「稳定前缀提交」策略(见 step1)。

## 范围

**做**:本地流式引擎(step1)、云端流式引擎(step2)、听写模式菜单与持久化、稳定前缀注入。
**明确不做**:不做退格回删式修正;不改现有 sentence 模式的行为;不做说话人分离。

## 任务分解

- [step1-本地流式.md](./step1-本地流式.md):sherpa-onnx OnlineRecognizer + streaming-paraformer 双语模型 + 稳定前缀提交。
- [step2-云端流式.md](./step2-云端流式.md):百炼 WebSocket 流式 + 断线重连降级。

## 依赖引入清单

- step1:无新依赖(sherpa-onnx 已在用,仅新增模型文件下载)。
- step2:WebSocket 客户端库(gorilla/websocket 或 nhooyr.io/websocket,引入前按 AGENTS.md 约定向用户说明)。

## 待决策项

1. 流式模式默认是否开启(当前倾向:默认仍 sentence,流式为体验选项)?
2. 稳定前缀的标点集与兜底字数(6 字)是否需要进 config?
3. step2 的 WebSocket 库选型。

## 环境变量变更

无(step1/step2 均复用现有 config 字段,新增 `dictation_mode`)。
