# voice-input 🎙️

**macOS 菜单栏语音听写工具** —— 按下热键,正常说话,停顿半秒,文字自动打进当前聚焦的输入框。

> Menu-bar voice dictation for macOS: speak, pause, and the text lands in whatever input field is focused. Local-first & offline-friendly.

![Go](https://img.shields.io/badge/Go-1.27+-00ADD8?logo=go&logoColor=white)
![macOS](https://img.shields.io/badge/macOS-13%2B%20Apple%20Silicon-000000?logo=apple&logoColor=white)
![License](https://img.shields.io/badge/License-MIT-green)

## 特性

- **双击即用**:打包成菜单栏应用,不占 Dock,开机即听
- **本地识别,隐私优先**:SenseVoice-Small 离线引擎,音频不出本机(M4 Pro 上 4 秒音频识别仅 77ms,快过实时 50 倍)
- **自动断句**:silero VAD 检测停顿,说完一句自动出一句,自带标点与数字规整
- **全局热键**:`Ctrl + Option + V` 随时开/关听写(暂停时释放麦克风,状态栏橙点消失)
- **双引擎,菜单一键切换**:本地 SenseVoice(离线、隐私、~100ms)⇄ 云端百炼 `qwen-audio-3.0-asr-flash`(复杂长句更准);云端密钥弹窗输入后**加密存入 macOS 钥匙串**,切换选择自动持久化
- **缺失模型自动下载**:首次运行引导下载,无需手动找模型
- **稳定签名方案**:本地证书签名,重编译不掉权限(详见下文"签名"一节)

## 演示

```
2026/09/18 22:33:11 🎙 10.55s 音频 → 199ms 识别:现在已经确认了,可以开始转入文字了
2026/09/18 22:33:26 🎙 14.36s 音频 → 257ms 识别:这个如果其他用户克隆下来的话,他们去打包
```

## 环境要求

| 项目 | 要求 | 说明 |
|---|---|---|
| 芯片 | Apple Silicon(M 系列) | 当前打包只含 arm64 库,Intel 支持待办 |
| 系统 | macOS 13+ | |
| 内存 | ≥ 4GB | 实际占用 < 1GB |
| 磁盘 | ~500MB | 模型 228MB + 应用 |
| 构建(仅开发者) | Go 1.27+ / Xcode 命令行工具 | 使用者零开发依赖 |

## 快速开始(开发者)

```bash
# 1. 安装依赖(仅构建者需要)
#    Go:      https://go.dev/dl  选 darwin-arm64.pkg
#    Xcode:   sudo xcodebuild -license accept
#    国内代理: go env -w GOPROXY=https://goproxy.cn,direct

# 2. 克隆并构建
git clone https://github.com/Yx0201/voice-input.git
cd voice-input
make setup    # 下载模型文件(约 230MB,自动走 hf-mirror 镜像)
make app      # 编译 + 打包 VoiceInput.app(首次自动生成本地签名证书)

# 3. 运行
open VoiceInput.app   # 或 Finder 双击
```

首次运行授权:**麦克风**(弹窗允许)+ **辅助功能**(应用会主动弹窗引导,去系统设置打开 VoiceInput 开关)。之后 `Ctrl+Option+V` 切换听写,对着任意输入框说话即可。

也可以用命令行方式体验:

```bash
./voice-input check          # 环境自检
./voice-input setup          # 手动补齐缺失模型
./voice-input file test.wav  # 转写单个 WAV 文件
./voice-input listen         # 终端常驻监听模式
```

## 模型文件说明

`make setup` / 应用内下载 / `voice-input setup` 三者等效,均为自动下载以下文件:

| 文件 | 大小 | 用途 | 手动下载(hf-mirror)| 手动下载(HuggingFace) |
|---|---|---|---|---|
| `model.int8.onnx` | 228M | SenseVoice-Small int8 识别模型(中/英/日/韩/粤语) | [链接](https://hf-mirror.com/csukuangfj/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17/resolve/main/model.int8.onnx) | [链接](https://huggingface.co/csukuangfj/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17/resolve/main/model.int8.onnx) |
| `tokens.txt` | 308K | 词表/分词文件 | [链接](https://hf-mirror.com/csukuangfj/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17/resolve/main/tokens.txt) | [链接](https://huggingface.co/csukuangfj/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17/resolve/main/tokens.txt) |
| `silero_vad.onnx` | 1.7M | 语音活动检测(断句) | [链接](https://hf-mirror.com/csukuangfj/vad/resolve/main/silero_vad.onnx) | [链接](https://huggingface.co/csukuangfj/vad/resolve/main/silero_vad.onnx) |

存放位置:

```
~/.voice_input/
├── config.json          # 可选,运行时配置
├── app.log              # 运行日志(排障第一现场)
└── models/
    ├── sense-voice/     # model.int8.onnx + tokens.txt
    └── vad/             # silero_vad.onnx
```

## 配置

`~/.voice_input/config.json`(全部可选,均有默认值):

```json
{
  "engine": "local",
  "input_device": "",
  "hotkey_modifiers": ["ctrl", "alt"],
  "hotkey_key": "v",
  "dashscope_api_key": "",
  "dashscope_workspace_id": "",
  "dashscope_model": "qwen-audio-3.0-asr-flash",
  "region": "cn-beijing"
}
```

- `engine`:`local`(默认)/ `cloud`(云端引擎)。菜单切换会自动写回此字段
- `input_device`:输入设备名子串匹配(如 `"AirPods"`);空 = 系统默认。启动日志会列出全部可用设备
- `hotkey_*`:全局热键,修饰键可选 `ctrl` / `alt`(Option)/ `shift` / `cmd`
- `dashscope_api_key`:更推荐留空——首次在菜单切换云端引擎时弹窗输入,自动加密保存到 macOS 钥匙串,不落明文文件
- `dashscope_workspace_id`:可选,百炼业务空间 ID(配置后走专属端点)
- 环境变量覆盖:`VOICE_INPUT_ENGINE`、`DASHSCOPE_API_KEY`、`DASHSCOPE_WORKSPACE_ID`、`VOICE_INPUT_MODEL_DIR`、`VOICE_INPUT_VAD_DIR`

### 云端引擎使用

1. 菜单栏 🎙 → **切换引擎** → **云端(百炼·高质量)**
2. 首次切换弹窗输入 API Key([百炼控制台](https://bailian.console.aliyun.com)获取,sk- 开头)→ 保存,自动存入钥匙串
3. 之后正常说话即可;随时切回本地(无需网络、完全离线)

云端引擎特点:识别质量更高(方言、专业词、长句),但音频会上云且需要网络;本地引擎全部在本机完成。

## macOS 权限与签名(重要,请阅读)

这类工具需要两项系统权限,都需要**一次性手动授权**:

| 权限 | 位置 | 用途 |
|---|---|---|
| 麦克风 | 系统设置 → 隐私与安全性 → 麦克风 | 采音 |
| 辅助功能 | 系统设置 → 隐私与安全性 → 辅助功能 | 全局热键 + 文字注入 |

**关于签名的坑(本项目已解决)**:macOS 的辅助功能授权与代码签名绑定。若使用 ad-hoc 临时签名,**每次重新编译签名指纹都会变,授权会静默失效**(开关显示开着但系统不认)。本项目通过 `scripts/make-dev-cert.sh` 在 `make app` 时自动生成**本机专属的稳定签名证书**(10 年有效,私钥不离开你的电脑),一次授权永久有效。首次构建时:

- 钥匙串可能弹窗询问"是否允许 codesign 访问密钥" → 点「**始终允许**」(仅这一次)
- 添加证书信任时可能要求输入一次电脑密码

## 分发给朋友(不构建的用户)

```bash
make dist    # 产出 VoiceInput-dist.zip(app + 模型 + 安装脚本)
```

朋友拿到 zip 后:解压 → 双击 `VoiceInput.app`(被拦则到 系统设置 → 隐私与安全性 → 点「仍要打开」)→ 缺模型时点菜单里的「⬇️ 下载缺失模型」→ 授权麦克风 + 辅助功能 → 使用。你重新构建发新版,对方授权不丢失。

> macOS 允许 App Store 之外私下分发应用(Apple 官方文档明确支持「仍要打开」路径);`$99` 开发者账号买的是无警告的分发体验,不是合法性。

## 工作原理

```
全局热键(自研 CGEventTap)→ 开/关听写
麦克风(malgo/CoreAudio,16kHz 单声道)
   → silero VAD 自动断句(静音 0.55s 成句,单句最长 10s)
   → ASR 引擎接口
       ├─ local:sherpa-onnx + SenseVoice-Small(进程内,~100ms 级)
       └─ cloud:百炼 qwen-audio(开发中)
   → CGEvent Unicode 键盘事件 → 打进当前聚焦的输入框
```

```
cmd/voice-input/          入口:app(菜单栏)/ listen / file / check / setup
internal/
  ├── asr/                Engine 接口 + local(SenseVoice)+ cloud(stub)+ WAV 读取
  ├── capture/            麦克风采集(malgo),支持设备选择
  ├── vad/                silero VAD 断句
  ├── hotkey/             自研 CGEventTap 全局热键(~80 行 C)
  ├── inject/             CGEvent 文字注入 + 辅助功能权限检查/引导
  ├── setup/              模型自动下载(hf-mirror 优先)
  └── config/             配置加载(json + 环境变量)
scripts/                  setup.sh / make-dev-cert.sh / install-models.command
packaging/Info.plist
```

## 性能实测

Apple M4 Pro / SenseVoice-Small int8 / 单线程 CPU:

| 音频时长 | 识别耗时 | 实时率 |
|---|---|---|
| 1.8s | 73ms | 0.04x |
| 10.6s | 199ms | 0.02x |
| 14.4s | 257ms | 0.02x |

## 常见问题

**双击应用没反应?** 看 `~/.voice_input/app.log`,应用会记录每次启动与死因。模型缺失时菜单栏会带错误提示出现并引导下载,不会静默退出。

**识别了但文字没打进输入框?** 辅助功能权限未生效:系统设置 → 隐私与安全性 → 辅助功能 → VoiceInput 开关打开。若开关已开仍无效,删除该条目重新添加一次(旧条目可能绑定了过期签名)。

**日志显示电平一直是 -80dBFS(静音)?** 采到了错误的输入设备。启动日志列出所有设备,在 config.json 的 `input_device` 里指定你实际说话用的设备(如 `"AirPods"`)。

**构建时卡在下载依赖?** `go mod tidy` 需要访问 GitHub,国内网络请走代理;或确认已设置 `GOPROXY=https://goproxy.cn,direct`。

**每次构建弹"codesign 想要访问钥匙串"?** 点「始终允许」,一次永久解决。

## 路线图

- [x] 本地引擎全链路(热键 → 采音 → VAD → 识别 → 注入)
- [x] 菜单栏应用 + 稳定签名 + 私域分发
- [x] 模型缺失自动下载
- [x] 云端引擎(qwen-audio-3.0-asr-flash,菜单栏一键切换,密钥存钥匙串)
- [ ] 按住说话(push-to-talk)模式
- [ ] Intel Mac 支持
- [ ] 正式 .icns 图标 / 开机自启

## 致谢

- [sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx) 及其预编译 Go 模块
- [SenseVoice](https://github.com/FunAudioLLM/SenseVoice)(阿里 FunAudioLLM)
- [malgo](https://github.com/gen2brain/malgo) / [systray](https://fyne.io) / [silero-vad](https://github.com/snakers4/silero-vad)

## License

[MIT](LICENSE)
