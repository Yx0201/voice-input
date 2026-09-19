// voice-input:macOS 全局语音听写工具(双引擎:本地 SenseVoice / 云端百炼)。
//
// 里程碑 1:check / file —— CLI 验证本地识别链路。
// 里程碑 2:listen —— 常驻监听,VAD 自动断句,识别结果直接打进焦点输入框。
// 里程碑 3:云端引擎(DashScope 原生多模态接口)。
// 里程碑 4:菜单栏常驻 + 引擎开关 + 首次启动内置下载器。
package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"voice_input/internal/asr"
	"voice_input/internal/capture"
	"voice_input/internal/config"
	"voice_input/internal/inject"
	"voice_input/internal/keystore"
	"voice_input/internal/overlay"
	"voice_input/internal/setup"
	"voice_input/internal/vad"
)

const sampleRate = 16000

func main() {
	// 双击 .app 启动时不带任何参数 —— 默认进入菜单栏模式
	if len(os.Args) < 2 {
		runApp()
		return
	}

	switch os.Args[1] {
	case "check":
		runCheck()
	case "file":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "用法: voice-input file <音频.wav>")
			os.Exit(2)
		}
		runFile(os.Args[2])
	case "listen":
		runListen()
	case "setup":
		runSetup()
	case "streamtest":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "用法: voice-input streamtest <音频.wav>(本地流式引擎离线验证)")
			os.Exit(2)
		}
		runStreamTest(os.Args[2])
	case "overlaytest":
		caretOK := overlay.SelfTest()
		if caretOK {
			fmt.Println("✅ 光标定位成功,loading 悬浮层已在光标旁显示 3 秒")
		} else {
			fmt.Println("⚠️ 光标定位失败(该应用不支持辅助功能定位),已用屏幕右下角兜底显示 3 秒")
		}
	case "app":
		runApp()
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `voice-input — macOS 全局语音听写工具(开发中)

用法:
  voice-input                菜单栏模式(默认;双击 .app 即此模式)
  voice-input setup          检查并下载缺失的模型文件(hf-mirror 优先)
  voice-input check          检查配置与模型文件是否就绪
  voice-input file <a.wav>   转写一个 WAV 文件(整句引擎)
  voice-input streamtest <a.wav>  流式引擎离线验证(打印 partial 演进与定稿)
  voice-input listen         常驻监听:说话→自动断句→文字打进焦点输入框
`)
}

// runStreamTest 用 WAV 文件离线验证流式引擎(本地或云端,按 config.Engine):
// 100ms 步进喂入,打印 partial 演进与端点定稿,模拟真实听写的出字节奏。
func runStreamTest(path string) {
	pcm, rate, err := asr.ReadWavFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	cfg := config.Load()
	var eng asr.StreamingEngine
	if cfg.Engine == "cloud" {
		if cfg.DashScopeAPIKey == "" {
			if k := keystore.Load(); k != "" {
				cfg.DashScopeAPIKey = k
			}
		}
		eng, err = asr.NewCloudStreamingEngine(cfg)
	} else {
		eng, err = asr.NewLocalStreamingEngine()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	sess, err := eng.NewSession()
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	defer sess.Close()
	fmt.Printf("[%s] 喂入 %d 采样 @%dHz(约 %.1fs),步进 100ms\n",
		eng.Name(), len(pcm), rate, float64(len(pcm))/float64(rate))

	chunk := rate / 10
	for i := 0; i < len(pcm); i += chunk {
		end := i + chunk
		if end > len(pcm) {
			end = len(pcm)
		}
		sess.Feed(pcm[i:end], rate)
		if (end/chunk)%5 == 0 { // 每 500ms 打一次 partial
			if p := sess.Partial(); p != "" {
				fmt.Printf("  [%.1fs] …%s\n", float64(end)/float64(rate), p)
			}
		}
		if sess.Endpoint() {
			fmt.Printf("  [%.1fs] ★定稿:%s\n", float64(end)/float64(rate), sess.Finalize())
		}
	}
	// 补静音并留时间等结果追赶,验证端点定稿(本地阈值约 1.4s;云端有网络滞后)
	silence := make([]float32, chunk)
	for i := 0; i < 60 && !sess.Endpoint(); i++ {
		sess.Feed(silence, rate)
		time.Sleep(100 * time.Millisecond)
	}
	if sess.Endpoint() {
		fmt.Printf("  [尾静音] ★定稿:%s\n", sess.Finalize())
	} else if p := sess.Partial(); p != "" {
		fmt.Printf("  [尾段] …%s(未定稿)\n", p)
	}
}

// runSetup 命令行下载/补齐缺失模型,带终端进度。
func runSetup() {
	cfg := config.Load()
	fmt.Printf("模型目录: %s\n", cfg.ModelDir)
	fmt.Printf("VAD 目录: %s\n", cfg.VADDir)

	lastFile, lastPct := "", -1
	err := setup.EnsureModels(cfg, func(file string, done, total int64) {
		if file != lastFile {
			fmt.Printf("⬇️  %s …\n", file)
			lastFile, lastPct = file, -1
		}
		if total > 0 {
			pct := int(done * 100 / total)
			if pct != lastPct {
				fmt.Printf("\r   %d%%", pct)
				lastPct = pct
			}
		} else {
			fmt.Printf("\r   %.1fMB", float64(done)/1e6)
		}
	})
	fmt.Println()
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✅ 模型文件全部就绪")
}

// runCheck 打印配置、模型文件存在性、引擎可用性。
func runCheck() {
	cfg := config.Load()
	fmt.Println("== voice-input 环境检查 ==")
	fmt.Printf("引擎:        %s\n", cfg.Engine)
	fmt.Printf("本地模型目录: %s\n", cfg.ModelDir)
	fmt.Printf("VAD 模型目录: %s\n", cfg.VADDir)
	fmt.Printf("云端模型:     %s\n", cfg.DashScopeModel)
	if cfg.DashScopeAPIKey != "" {
		fmt.Printf("百炼密钥:     已配置(来源: env 或 config.json)\n")
	} else {
		fmt.Printf("百炼密钥:     未配置(仅影响云端引擎)\n")
	}
	fmt.Println()

	if _, err := asr.NewEngine(cfg); err != nil {
		fmt.Printf("❌ 引擎不可用: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✅ 当前引擎加载成功,可以运行: voice-input file <a.wav> 或 voice-input listen")
}

// runFile 用当前引擎转写一个 WAV 文件并打印结果与耗时。
func runFile(path string) {
	pcm, sampleRateFile, err := asr.ReadWavFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	duration := time.Duration(len(pcm)) * time.Second / time.Duration(sampleRateFile)
	fmt.Printf("音频: %d 采样 @ %dHz(约 %v)\n", len(pcm), sampleRateFile, duration.Round(time.Millisecond))

	engine, err := asr.NewEngine(config.Load())
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}

	start := time.Now()
	text, err := engine.Transcribe(pcm, sampleRateFile)
	elapsed := time.Since(start)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ 转写失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[%s] 耗时 %v(实时率 %.2fx)\n", engine.Name(), elapsed.Round(time.Millisecond),
		float64(elapsed)/float64(duration))
	fmt.Printf("---- 识别结果 ----\n%s\n", text)
}

// runListen 常驻监听主循环:麦克风 → VAD 断句 → ASR → 打进焦点输入框。
func runListen() {
	cfg := config.Load()

	engine, err := asr.NewEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	detector, err := vad.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}

	fmt.Println("== voice-input 常驻监听 ==")
	fmt.Printf("引擎: %s\n", engine.Name())
	fmt.Println()
	fmt.Println("⚠️  你说的每一句话都会被识别并打进当前聚焦的输入框。")
	fmt.Println("    首次运行需授权:麦克风 + 辅助功能(系统设置 → 隐私与安全性)。")
	fmt.Println("    Ctrl-C 退出。")
	fmt.Println()

	// 音频线程 → 主循环(满则丢弃,防止 VAD/ASR 阻塞采音)
	audioCh := make(chan []float32, 128)

	mic, err := capture.NewMic(sampleRate, 1, config.Load().InputDevice, func(pcm []float32) {
		select {
		case audioCh <- pcm:
		default:
		}
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	defer mic.Close()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	fmt.Println("🎤 监听中……开始说话吧(静音半秒即成句)")

	for {
		select {
		case <-sigCh:
			fmt.Println("\n退出。")
			return
		case pcm := <-audioCh:
			for _, seg := range detector.Feed(pcm) {
				transcribeAndType(engine, seg)
			}
		}
	}
}

// transcribeAndType 转写一段语音并注入焦点输入框,打印状态行。
// 识别期间在光标旁显示 loading 悬浮层(告知"AI 在转,不是卡死")。
func transcribeAndType(engine asr.Engine, seg []float32) {
	done := overlay.Busy()
	defer done()

	dur := time.Duration(len(seg)) * time.Second / sampleRate
	start := time.Now()
	text, err := engine.Transcribe(seg, sampleRate)
	elapsed := time.Since(start)
	if err != nil {
		log.Printf("❌ 转写失败: %v", err)
		return
	}
	log.Printf("🎙 %v 音频 → %v 识别:%s", dur.Round(time.Millisecond), elapsed.Round(time.Millisecond), text)
	if text != "" {
		if !inject.IsAccessibilityGranted() {
			log.Printf("⚠️ 文字未注入:辅助功能权限未生效(系统设置→隐私与安全性→辅助功能→删除 VoiceInput 条目后重新授权)")
			return
		}
		inject.TypeText(text)
	}
}
