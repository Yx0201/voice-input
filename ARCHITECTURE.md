# ARCHITECTURE.md — voice-input 技术架构

> macOS 菜单栏语音听写工具:按热键说话,VAD/流式断句,识别文字打进当前聚焦的输入框。
> 双引擎(本地 SenseVoice / 云端百炼)可插拔。源码分发,用户自行构建。

## 技术栈【已确认】

| 层 | 选型 | 备注 |
|---|---|---|
| 语言 | Go 1.27 + CGO | 全 cgo 栈,只支持 macOS |
| ASR(本地) | sherpa-onnx v1.13.8 + SenseVoice-Small int8 | 经平台模块 `github.com/k2-fsa/sherpa-onnx-go-macos`(绑定+双架构 dylib 同模块,`go get` 即部署) |
| ASR(云端) | 百炼 `qwen-audio-3.0-asr-flash` | DashScope 原生多模态 HTTP 接口,非 OpenAI 兼容端点 |
| VAD | silero-vad ONNX(sherpa-onnx 内置) | 静音 0.55s 成句,单句最长 10s |
| 麦克风 | malgo(gen2brain/malgo,CoreAudio) | 16kHz 单声道 int16→float32,支持设备名子串选择 |
| 全局热键 | 自研 CGEventTap(internal/hotkey,~80 行 C) | 第三方库在菜单栏环境下事件不投递,已弃用 |
| 文字注入 | 自研 CGEvent Unicode 键盘事件(internal/inject) | 零依赖替代 robotgo;20 字符分块投递 |
| 菜单栏 | fyne.io/systray | LSUIElement 应用,不占 Dock |
| 密钥存储 | macOS 钥匙串(经 `security` CLI) | API Key 不落明文 |
| 签名 | 自签稳定证书 `voiceinput-dev`(10 年) | scripts/make-dev-cert.sh 幂等生成,make app 自动调用 |

## 听写管线架构【已确认】

```
麦克风(malgo, 音频线程回调→channel,满则丢帧保采音)
   │
   ├─「完整句模式」(现状默认):silero VAD 断句 → 整段送 Engine.Transcribe → 定稿全文注入
   │
   └─「即时出字模式」(specCoding/流式听写,未实施):音频直喂 StreamingSession
        → Partial() 稳定前缀增量注入 → Endpoint() 定稿补打
```

- 三条并发线:音频回调(实时线程)、audioLoop(消费/VAD/识别/注入)、菜单+热键(主 runloop)。共享状态用 atomic/mutex。
- 引擎热切换:菜单即时替换 dictation.engine(RWMutex),正在识别的句子由旧引擎完成。
- 文字注入前检查 `inject.IsAccessibilityGranted()`,失败写日志提示,不静默。

## 引擎架构【已确认】

```go
// internal/asr
type Engine interface {
    Name() string
    Transcribe(pcm []float32, sampleRate int) (string, error)
}
```

- **local**:SenseVoice int8,开 UseInverseTextNormalization(标点+数字规整),CPU 2 线程,M4 Pro 实测 4s 音频 77ms(RTF 0.02x)。
- **cloud**:PCM→WAV(自写 44 字节头)→base64 data URI→`POST /api/v1/services/aigc/multimodal-generation/generation`(公共端点,或配置 workspace_id 走 `{ws}.{region}.maas` 专属端点),响应 `output.text`。假 key 可得结构化 `InvalidApiKey` 错误(协议级验证手段)。
- **启动策略 initBestEngine**:按 config.Engine 加载;失败自动尝试另一引擎(如本地模型缺失但钥匙串有 Key→自动云端)。两者皆无→不占麦克风,菜单引导配置。
- 配置了流式后将新增 `StreamingEngine/StreamingSession` 接口,与 Engine 正交(见 specCoding/流式听写)。

## 权限与签名架构【已确认,2026-09-18 定型】

1. **麦克风(TCC)**:Info.plist 带 NSMicrophoneUsageDescription,首次弹窗;辅助功能请求刻意排在麦克风授权**之后**发起,避免两个系统弹窗互相顶掉。
2. **辅助功能**:macOS 对此类权限**默认不自动弹窗**,应用需主动调 `AXIsProcessTrustedWithOptions(kAXTrustedCheckOptionPrompt)`(inject.RequestAccessibility);菜单常驻「请求辅助功能授权…」可随时重新唤起。
3. **稳定签名(关键)**:TCC 授权绑定代码签名。ad-hoc 签名每次编译指纹变化→授权静默失效(开关显示开着但 `AXIsProcessTrusted()==false`,且**切换开关不会刷新绑定,必须删条目或 `tccutil reset` 重来**)。解法:`make app` 自动用钥匙串里的 `voiceinput-dev` 自签证书(稳定身份)签名,一次授权跨重编译有效。构建弹"codesign 想访问钥匙串"→点「始终允许」一次。
4. **密钥**:云端 API Key 经 osascript 弹窗(隐藏输入)收集→钥匙串 generic-password(service=voice-input, account=dashscope)加密存储;启动/切换时自动补读。

## 分发架构【已确认】

- **源码路线**(GitHub):clone → make setup(模型,hf-mirror 优先)→ make app(构建+自签证书+打包)→ 双击。贡献者的证书各机独立生成,私钥永不入库。
- **私域路线**(make dist):`VoiceInput-dist.zip` = .app + models + Install.command(非破坏性,仅缺失时复制并验证)。接收方:解压→(可跳过脚本直接双击 app,缺模型点菜单下载;云端用户仅需 1.7MB VAD)→Gatekeeper「仍要打开」(macOS 官方支持的店外分发路径)→ 授权一次。更新=替换 .app,授权不丢(稳定签名)。
- **.app 自包含**:sherpa 双 dylib 拷入 Contents/Frameworks,rpath 重定向为 `@executable_path/../Frameworks`,脱离 Go 模块缓存;ad-hoc 兜底签名仅在无证书时。

## 配置体系【已确认】

`~/.voice_input/config.json`(全部可选;PatchConfig 按 map 合并写回,用于持久化菜单运行时选择):

| 字段 | 默认 | 说明 |
|---|---|---|
| engine | local | local / cloud |
| dictation_mode | sentence | sentence / streaming(未实施) |
| input_device | ""(系统默认) | 设备名子串匹配;启动日志列出全部设备 |
| hotkey_modifiers / hotkey_key | [ctrl,alt] / v | 可选 ctrl/alt/shift/cmd + 单字母数字 |
| dashscope_api_key | "" | 推荐留空走钥匙串 |
| dashscope_workspace_id | "" | 配置后走业务空间专属端点 |
| dashscope_model / region | qwen-audio-3.0-asr-flash / cn-beijing | |

环境变量覆盖:`VOICE_INPUT_ENGINE`、`VOICE_INPUT_MODEL_DIR`、`VOICE_INPUT_VAD_DIR`、`DASHSCOPE_API_KEY`、`DASHSCOPE_WORKSPACE_ID`。

数据目录:`~/.voice_input/`(models/、app.log、config.json)。app.log 为全实例共享追加,排障第一现场。

## 目标目录结构【已确认】

```
cmd/voice-input/       入口:无参=菜单栏 app;子命令 listen/file/check/setup
internal/
  asr/                 Engine 接口 + local/cloud 实现 + WAV 编解码
  capture/             malgo 采音 + 设备枚举/选择
  vad/                 silero 断句(Reset=整体重建,不用 Clear)
  hotkey/              自研 CGEventTap(tap.c/tap.h + //export 桥)
  inject/              CGEvent 注入 + 辅助功能检查/引导
  keystore/            钥匙串读写(security CLI)
  setup/               模型下载(hf-mirror→HF 回退,.part+最小体积校验)
  config/              配置加载 + PatchConfig
scripts/               setup.sh / make-dev-cert.sh / install-models.command
packaging/Info.plist   LSUIElement=true, NSMicrophoneUsageDescription
Makefile               setup/build/app/dist;GO 用绝对路径变量
specCoding/ Memory/    规格与进度存档(见 AGENTS.md 规则)
```

## 编码约定【已确认】

- 日志统一标准 log(已 `log.SetOutput` 进 app.log+stderr);GUI 模式下 stderr 不可见,新日志点必须走标准 log 而非自建 logger 或 fmt。
- 中文注释与日志;错误信息面向用户可读(含下一步动作提示)。
- 音频线程回调只投递 channel;重活全部在消费侧 goroutine。
- 菜单文案即状态机显示:setListening/switchEngine 等状态变更后必须 refreshMenu/syncEngineMenu 保持一致。

## 已知约束与平台坑【已确认,编码时须处理】

- **sherpa-onnx v1.13.8 绑定形态**(以 `$(go env GOMODCACHE)/github.com/k2-fsa/sherpa-onnx-go-macos@v1.13.8` 为准,勿信旧博客):绑定在平台模块根目录;`NewOfflineRecognizer` 只返回指针(nil=失败);stream 用包级 `NewOfflineStream(recognizer)`;`GetResult()` 挂在 stream 上;`OfflineModelConfig.Tokens` 与 SenseVoice 槽位;VAD 的 `Clear()` 会致后续永不出段,Reset 需整体重建实例。
- **systray**:模块路径是 `fyne.io/systray`;无顶层 AddSubMenu,子菜单走 `AddMenuItem(...).AddSubMenuItemCheckbox(...)`。
- **cgo**:`//export` 名必须与 Go 函数名一致;CGEvent 相关需同时链 CoreGraphics+CoreFoundation;热键键码非连续(查 internal/hotkey 的 keyCodes 表)。
- **构建**:GNU Make 3.81 忽略 `export PATH :=`;OpenSSL3 的 pkcs12 默认算法进不了钥匙串,须 `-legacy`;codesign 会把已签名 dylib 置只读,打包目标先 rm -rf 重建 bundle。
- **LaunchServices**:双击 .app 启动**不带 argv**(入口必须处理无参);对已运行实例 `open` 仅激活不重启。
- **zsh**:用户侧命令带未匹配通配符会整条中止(NOMATCH)。
