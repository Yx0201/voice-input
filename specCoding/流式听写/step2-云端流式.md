# step2 · 云端流式引擎(百炼 WebSocket)

> 状态:**草案**(2026-09-19) | 依赖:step1 的模式框架 | 前置:WebSocket 库选型(待用户确认)

## 现状盘点

- 云端整句引擎已上线(qwen-audio-3.0-asr-flash,HTTP,input_audio base64),Key 存钥匙串。
- 百炼提供同族流式服务:`qwen-audio-3.0-asr-flash-streaming`,WebSocket 协议:
  连接 → `run-task` → 客户端持续推音频帧 → 服务端流式返回(sentence.text 增量,句/字级时间戳)→ `finish-task`。
- 文档:help.aliyun.com/zh/model-studio/fun-asr-realtime-websocket-api(客户端/服务端事件各一页)。

## 任务分解

1. **WebSocket 客户端**(internal/asr/cloudstream.go):实现 step1 的 StreamingSession 接口;
   - 会话生命周期 = 一个 WebSocket 任务;Feed 推音频帧(PCM16 wav 分片),收 sentence.text 更新 Partial;
   - Endpoint 语义:服务端 sentence_end 或本地静音兜底;
2. **连接管理**:断线指数退避重连;连续 3 次失败→状态栏提示并降级当前引擎整句路径;
3. **菜单**:「切换引擎」子菜单新增「云端·流式」项(与本地/云端整句并列);计费提示(tooltip 注明按输入秒计费);
4. **日志**:连接/断开/降级事件必打;文本不逐帧打。

## 依赖引入清单

- WebSocket 库:gorilla/websocket(社区事实标准,纯 Go)或 nhooyr.io/websocket(更现代)。**引入前按 AGENTS.md 向用户说明选型**。

## 验收标准

1. 流式出字体验与 step1 一致(边说边出、停顿定稿);
2. 网络中断后 10 秒内自动重连,重连期间语音不丢句或明确提示;
3. Key 复用钥匙串,无重复输入;
4. 连续说话 5 分钟无内存泄漏、无连接僵死。

## 待决策项

1. WebSocket 库选型;2. 音频推流帧长(建议 100-200ms)与格式(wav 裸帧 vs base64);3. 计费提示展示形式。

## 实施记录

- (待开工)
