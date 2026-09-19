package main

// 菜单栏常驻模式(app 子命令,.app 双击后的默认入口):
// - 菜单栏图标常驻,显示听写状态,菜单可手动 开启/暂停/退出
// - 全局组合键(默认 Ctrl+Option+V)随时切换听写开/关
// - 开启时:麦克风→VAD 断句→ASR→打进焦点输入框
// - 暂停时:释放麦克风(系统橙点消失),丢弃未完结的半句
//
// 容错设计:模型缺失不再静默退出——菜单栏照常出现,
// 状态行提示"模型缺失",菜单提供一键下载,完成后自动开始听写。

import (
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/systray"

	"voice_input/internal/asr"
	"voice_input/internal/capture"
	"voice_input/internal/config"
	"voice_input/internal/hotkey"
	"voice_input/internal/inject"
	"voice_input/internal/keystore"
	"voice_input/internal/setup"
	"voice_input/internal/vad"
)

// appLog 双击运行时 stdout 不可见,所有输出同步落到日志文件。
var appLog *log.Logger

func setupAppLog() {
	dir := config.DefaultDir()
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "app.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		appLog = log.New(os.Stderr, "", log.LstdFlags)
		return
	}
	appLog = log.New(io.MultiWriter(os.Stderr, f), "", log.LstdFlags)
	// 关键:把标准 log(识别/错误行用的是它)也重定向进文件,
	// 否则 GUI 模式下全部写进 /dev/null,造成"识别没发生"的假象
	log.SetOutput(io.MultiWriter(os.Stderr, f))
}

// dictation 持有听写应用的全部运行态。
type dictation struct {
	cfg      config.Config
	engine   asr.Engine // 整句引擎;engineMu 保护(菜单可热切换)
	engineMu sync.RWMutex
	detector *vad.Detector
	mic      *capture.Mic
	audioCh  chan []float32
	listening atomic.Bool
	micReady  atomic.Bool
	axRequested atomic.Bool
	engineReady atomic.Bool
	downloading  atomic.Bool

	// 流式听写(即时出字)
	streamingMode atomic.Bool
	streamEng     asr.StreamingEngine // 当前引擎对应的流式实现(懒加载)
	streamSession asr.StreamingSession
	streamMu      sync.Mutex // 保护 session/committed/lastPartial
	committed     string     // 已打进输入框的稳定前缀
	lastPartial   string     // 上一次 partial(两次一致视为稳定)

	mStatus   *systray.MenuItem
	mToggle   *systray.MenuItem
	mDownload *systray.MenuItem
	mEngLocal *systray.MenuItem
	mEngCloud *systray.MenuItem
	mModeStream   *systray.MenuItem
	mModeSentence *systray.MenuItem
	mTrigToggle *systray.MenuItem
	mTrigPtt    *systray.MenuItem

	// 触发方式:toggle(组合键切换)/ ptt(按住说话)
	hotkeyMode    atomic.Value // string
	hotkeyRestart chan struct{}
}

// currentEngine 读取当前引擎(线程安全)。
func (d *dictation) currentEngine() asr.Engine {
	d.engineMu.RLock()
	defer d.engineMu.RUnlock()
	return d.engine
}

// setEngine 替换当前引擎(线程安全);正在识别的句子由旧引擎完成,新句走新引擎。
func (d *dictation) setEngine(e asr.Engine) {
	d.engineMu.Lock()
	d.engine = e
	d.engineMu.Unlock()
}

// runApp 启动菜单栏应用;阻塞直至退出。
func runApp() {
	setupAppLog()
	appLog.Printf("== voice-input 启动(版本 0.1.0)==")

	cfg := config.Load()
	appLog.Printf("配置: engine=%s model=%s hotkey=%v+%s",
		cfg.Engine, cfg.ModelDir, cfg.HotkeyModifiers, cfg.HotkeyKey)

	d := &dictation{
		cfg:           cfg,
		audioCh:       make(chan []float32, 128),
		hotkeyRestart: make(chan struct{}, 1),
	}
	d.hotkeyMode.Store(cfg.HotkeyMode)

	// 引擎失败不退出:菜单栏起来后引导配置;任一引擎可用即自动开麦
	d.streamingMode.Store(cfg.DictationMode != "sentence")
	if d.streamingMode.Load() {
		if err := d.initStreaming(); err != nil {
			appLog.Printf("⚠️ 流式引擎未就绪: %v", err)
		} else {
			appLog.Printf("流式引擎就绪: %s", d.streamEng.Name())
		}
	} else if d.initBestEngine() {
		appLog.Printf("引擎就绪: %s", d.currentEngine().Name())
	} else {
		appLog.Printf("⚠️ 本地模型与云端 Key 均未配置——不自动开麦,启动后引导配置")
	}

	if names, err := capture.ListCaptureDeviceNames(); err == nil {
		appLog.Printf("可用输入设备: %s(当前选择: %s)",
			strings.Join(names, " | "), orDefault(cfg.InputDevice, "系统默认"))
	}

	go d.audioLoop()
	systray.Run(d.onReady, func() {})
	appLog.Printf("== voice-input 退出 ==")
}

// initStreaming 按当前引擎选择初始化流式引擎(懒加载,失败可重试)。
func (d *dictation) initStreaming() error {
	if d.cfg.Engine == "cloud" {
		if d.cfg.DashScopeAPIKey == "" {
			if k := keystore.Load(); k != "" {
				d.cfg.DashScopeAPIKey = k
			}
		}
		eng, err := asr.NewCloudStreamingEngine(d.cfg)
		if err != nil {
			return err
		}
		d.streamEng = eng
		return nil
	}
	eng, err := asr.NewLocalStreamingEngine()
	if err != nil {
		return err
	}
	d.streamEng = eng
	return nil
}

// setDictationMode 菜单切换听写模式(流式⇄整句),即时生效并持久化。
func (d *dictation) setDictationMode(mode string) {
	streaming := mode != "sentence"
	if streaming == d.streamingMode.Load() {
		d.syncModeMenu()
		return
	}
	wasListening := d.listening.Load()
	if wasListening {
		d.setListening(false) // 收尾当前会话
	}
	d.streamingMode.Store(streaming)
	if streaming {
		if err := d.initStreaming(); err != nil {
			appLog.Printf("⚠️ 流式引擎未就绪: %v", err)
			if d.mDownload != nil {
				d.mDownload.Show()
			}
		} else {
			appLog.Printf("⚡ 已切换到即时出字模式(%s)", d.streamEng.Name())
		}
	} else {
		if err := d.initBestEngine(); err == false {
			appLog.Printf("⚠️ 整句引擎未就绪")
		} else {
			appLog.Printf("📝 已切换到完整句模式(%s)", d.currentEngine().Name())
		}
	}
	d.syncModeMenu()
	_ = config.PatchConfig(map[string]any{"dictation_mode": mode})
	if wasListening {
		d.setListening(true)
	}
}

// syncModeMenu 让模式子菜单勾选与实际一致。
func (d *dictation) syncModeMenu() {
	if d.mModeStream == nil {
		return
	}
	if d.streamingMode.Load() {
		d.mModeStream.Check()
		d.mModeSentence.Uncheck()
	} else {
		d.mModeSentence.Check()
		d.mModeStream.Uncheck()
	}
}

// ---- 流式核心:喂音频 → 稳定前缀增量注入 → 端点定稿 ----

var streamPuncts = "。?!;;、,.!?"

// streamFeed 流式模式处理一帧音频。
func (d *dictation) streamFeed(pcm []float32) {
	d.streamMu.Lock()
	defer d.streamMu.Unlock()

	if d.streamSession == nil {
		return // 会话在 setListening(true) 创建;此处防御
	}
	d.streamSession.Feed(pcm, sampleRate)
	d.commitStable(d.streamSession.Partial())

	if d.streamSession.Endpoint() {
		final := d.streamSession.Finalize()
		d.commitExact(final)
		d.committed, d.lastPartial = "", ""
		if final != "" {
			appLog.Printf("🎙 定稿:%s", final)
		}
	}
}

// commitStable 计算 partial 的稳定前缀并增量注入(永不回删)。
// 稳定判定:① 最后一个句读标点之前(含);② 连续两次 partial 公共前缀(≥2 新字);
// ③ 兜底:未提交部分超过 6 字时整段提交(防无标点长句迟迟不出字)。
func (d *dictation) commitStable(partial string) {
	if partial == "" {
		return
	}
	stable := ""
	if i := strings.LastIndexAny(partial, streamPuncts); i >= 0 {
		stable = partial[:i+1]
	}
	if stable == "" {
		if cp := commonPrefix(d.lastPartial, partial); len([]rune(cp))-len([]rune(d.committed)) >= 2 {
			stable = cp
		}
	}
	if stable == "" && len([]rune(partial))-len([]rune(d.committed)) >= 6 {
		stable = partial
	}
	d.lastPartial = partial

	if len(stable) > len(d.committed) && strings.HasPrefix(stable, d.committed) {
		delta := stable[len(d.committed):]
		if delta != "" && inject.IsAccessibilityGranted() {
			inject.TypeText(delta)
			d.committed = stable
		}
	}
}

// commitExact 端点定稿:只追加不回删;定稿与已提交前缀不一致时保留已提交并记日志。
func (d *dictation) commitExact(final string) {
	final = strings.TrimSpace(final)
	if final == "" {
		return
	}
	if strings.HasPrefix(final, d.committed) {
		if delta := final[len(d.committed):]; delta != "" && inject.IsAccessibilityGranted() {
			inject.TypeText(delta)
		}
	} else if d.committed == "" {
		if inject.IsAccessibilityGranted() {
			inject.TypeText(final)
		}
	} else {
		appLog.Printf("⚠️ 定稿与已提交不一致(保留已提交):已=%q 定=%q", d.committed, final)
	}
}

// initBestEngine 按配置加载引擎;主选择不可用时自动尝试另一个
// (如本地模型缺失但钥匙串存有云端 Key,则自动回退云端)。
// 返回是否成功。成功后 detector 也就绪;失败时引擎与 VAD 均未就绪。
func (d *dictation) initBestEngine() bool {
	if err := d.initEngine(); err == nil {
		return true
	}

	other := "cloud"
	if d.cfg.Engine == "cloud" {
		other = "local"
	} else if d.cfg.DashScopeAPIKey == "" {
		if k := keystore.Load(); k != "" {
			d.cfg.DashScopeAPIKey = k
		}
	}

	old := d.cfg.Engine
	d.cfg.Engine = other
	if err := d.initEngine(); err != nil {
		d.cfg.Engine = old
		return false
	}
	d.syncEngineMenu(other)
	appLog.Printf("↩️ 主引擎不可用,已自动切换到 %s 引擎", other)
	return true
}

// initEngine 加载(或重载)识别引擎与 VAD;成功后 audioLoop 开始消费音频。
func (d *dictation) initEngine() error {
	// 云端引擎:密钥可存于钥匙串,启动时补读
	if d.cfg.Engine == "cloud" && d.cfg.DashScopeAPIKey == "" {
		if k := keystore.Load(); k != "" {
			d.cfg.DashScopeAPIKey = k
		}
	}
	engine, err := asr.NewEngine(d.cfg)
	if err != nil {
		return err
	}
	detector, err := vad.New(d.cfg)
	if err != nil {
		return err
	}
	d.setEngine(engine)
	d.detector = detector
	d.engineReady.Store(true)
	return nil
}

// onReady 配置菜单栏(systray 就绪后回调)。
func (d *dictation) onReady() {
	systray.SetTemplateIcon(menuIcon, menuIcon)
	systray.SetTooltip("voice-input 语音听写")

	d.mStatus = systray.AddMenuItem("启动中……", "当前状态")
	d.mStatus.Disable()
	d.mToggle = systray.AddMenuItem("暂停听写", "点击或按热键切换")
	d.mDownload = systray.AddMenuItem("⬇️ 下载缺失模型(约 230MB)", "缺失时显示;下载完成自动开始听写")

	// 引擎切换子菜单(本地/云端二选一)
	mEng := systray.AddMenuItem("切换引擎", "本地离线 / 云端高质量")
	d.mEngLocal = mEng.AddSubMenuItemCheckbox("本地(SenseVoice·离线)", "无需网络,音频不出本机", d.cfg.Engine != "cloud")
	d.mEngCloud = mEng.AddSubMenuItemCheckbox("云端(百炼·高质量)", "首次切换弹窗输入 API Key,存入钥匙串", d.cfg.Engine == "cloud")

	// 听写模式子菜单(即时出字/完整句)
	mMode := systray.AddMenuItem("听写模式", "即时出字(流式) / 完整句(更准)")
	d.mModeStream = mMode.AddSubMenuItemCheckbox("即时出字(流式)", "边说边出字,落后语音约半秒", d.streamingMode.Load())
	d.mModeSentence = mMode.AddSubMenuItemCheckbox("完整句(更准)", "说完一句再出字,识别更稳", !d.streamingMode.Load())

	// 触发方式子菜单(组合键切换/按住说话)
	ptt := d.hotkeyMode.Load() == "ptt"
	mTrig := systray.AddMenuItem("触发方式", "组合键切换 / 按住说话")
	d.mTrigToggle = mTrig.AddSubMenuItemCheckbox("组合键切换(Ctrl+Option+V)", "按一下开,再按一下关", !ptt)
	d.mTrigPtt = mTrig.AddSubMenuItemCheckbox("按住说话(Option+空格)", "按住收音,松开结束;按键不会向输入框打出空格", ptt)

	mAX := systray.AddMenuItem("请求辅助功能授权…", "热键与文字注入需要;弹窗被顶掉时可点这里重新唤起")
	mHelp := systray.AddMenuItem("❓ 使用帮助", "配置指引:本地模型下载 / 云端 Key 申请")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出", "")

	go func() {
		for {
			select {
			case <-d.mToggle.ClickedCh:
				d.toggle()
			case <-d.mDownload.ClickedCh:
				go d.downloadAndInit()
			case <-d.mEngLocal.ClickedCh:
				go d.switchEngine("local")
			case <-d.mEngCloud.ClickedCh:
				go d.switchEngine("cloud")
			case <-d.mModeStream.ClickedCh:
				go d.setDictationMode("streaming")
			case <-d.mModeSentence.ClickedCh:
				go d.setDictationMode("sentence")
			case <-d.mTrigToggle.ClickedCh:
				go d.setHotkeyMode("toggle")
			case <-d.mTrigPtt.ClickedCh:
				go d.setHotkeyMode("ptt")
			case <-mHelp.ClickedCh:
				go d.showHelp()
			case <-mAX.ClickedCh:
				go func() {
					if inject.RequestAccessibility() {
						appLog.Printf("✅ 辅助功能权限已授予")
					} else {
						appLog.Printf("⚠️ 已再次请求辅助功能授权,请到 系统设置→隐私与安全性→辅助功能 启用 VoiceInput")
					}
				}()
			case <-mQuit.ClickedCh:
				if d.micReady.Load() {
					d.mic.Close()
				}
				systray.Quit()
			}
		}
	}()

	// 热键在 systray 的 runloop 就绪后注册
	go d.hotkeyLoop()

	// 启动即用策略:任一引擎就绪则自动开麦;两者皆无则待配置,不占麦克风
	// 例外:按住说话(ptt)模式下保持静默,等用户按键
	if (d.streamingMode.Load() && d.streamEng != nil) || (!d.streamingMode.Load() && d.currentEngine() != nil) {
		// 下载入口可见性:当前模式所需模型齐备才隐藏(流式还需流式模型)
		needDownload := !setup.ModelsReady(d.cfg) ||
			(d.streamingMode.Load() && d.cfg.Engine != "cloud" && !setup.StreamingModelsReady())
		if !needDownload {
			d.mDownload.Hide()
		}
		if d.hotkeyMode.Load() == "ptt" {
			d.mStatus.SetTitle("🎙 按住 Option+空格 说话")
			d.mToggle.SetTitle("开启听写(手动)")
		} else {
			go d.enableListening()
		}
	} else {
		d.mStatus.SetTitle("⚠️ 未配置引擎——见「❓ 使用帮助」")
		d.mToggle.SetTitle("开启听写(需先配置)")
	}
}

// downloadAndInit 下载缺失模型并初始化引擎,成功后自动开始听写。
func (d *dictation) downloadAndInit() {
	if !d.downloading.CompareAndSwap(false, true) {
		return
	}
	defer d.downloading.Store(false)

	appLog.Printf("⬇️ 开始下载缺失模型(hf-mirror 优先)")
	d.mStatus.SetTitle("⬇️ 下载模型中……")

	err := setup.EnsureModels(d.cfg, func(file string, done, total int64) {
		if total > 0 {
			pct := done * 100 / total
			d.mStatus.SetTitle(fmt.Sprintf("⬇️ %s %d%%", file, pct))
		} else {
			d.mStatus.SetTitle(fmt.Sprintf("⬇️ %s %.1fMB", file, float64(done)/1e6))
		}
	})
	if err != nil {
		appLog.Printf("❌ 模型下载失败: %v", err)
		d.mStatus.SetTitle("❌ 下载失败——检查网络后重试(日志见 ~/.voice_input/app.log)")
		return
	}

	if err := d.initEngine(); err != nil {
		appLog.Printf("❌ 模型下载完成但引擎加载失败: %v", err)
		d.mStatus.SetTitle("❌ 引擎加载失败(日志见 ~/.voice_input/app.log)")
		return
	}
	d.mDownload.Hide()
	appLog.Printf("✅ 模型就绪,引擎: %s", d.engine.Name())
	d.setListening(true)
}

// enableListening 首次开启听写;麦克风授权完成后才发起辅助功能请求,
// 避免两个系统弹窗叠在一起互相顶掉。
func (d *dictation) enableListening() {
	d.setListening(true)

	if d.axRequested.CompareAndSwap(false, true) {
		if inject.RequestAccessibility() {
			appLog.Printf("✅ 辅助功能权限已授予")
		} else {
			appLog.Printf("⚠️ 已请求辅助功能授权(弹窗);启用后注入立即生效,热键需重启应用")
		}
	}
}

// switchEngine 菜单触发的引擎热切换。切换云端时按需弹窗收集 API Key 并存钥匙串;
// 切换本地缺模型时给出安装指引;选择持久化到 config.json。
func (d *dictation) switchEngine(which string) {
	switch which {
	case "cloud":
		if d.cfg.DashScopeAPIKey == "" {
			d.cfg.DashScopeAPIKey = keystore.Load()
		}
		if d.cfg.DashScopeAPIKey == "" {
			key, ok := promptAPIKey()
			if !ok {
				appLog.Printf("已取消输入密钥,保持原引擎")
				d.showCloudGuide()
				d.syncEngineMenu(engineName(d))
				return
			}
			if err := keystore.Save(key); err != nil {
				appLog.Printf("⚠️ 密钥存入钥匙串失败(%v),本次会话仍可用,重启后需重新输入", err)
			} else {
				appLog.Printf("🔑 API Key 已加密保存到钥匙串")
			}
			d.cfg.DashScopeAPIKey = key
		}
		eng, err := asr.NewCloudEngine(d.cfg)
		if err != nil {
			appLog.Printf("❌ 云端引擎不可用: %v", err)
			d.mStatus.SetTitle("❌ 云端引擎:" + err.Error())
			d.syncEngineMenu(engineName(d))
			return
		}
		// 云端也需要 VAD 断句模型(仅 1.7MB),缺失则自动补下,无需 228MB 主模型
		if d.detector == nil {
			if err := d.ensureVAD(); err != nil {
				appLog.Printf("❌ VAD 模型下载失败: %v", err)
				d.mStatus.SetTitle("❌ 断句模型下载失败,检查网络后重试")
				return
			}
		}
		d.setEngine(eng)
		d.engineReady.Store(true)
		d.syncEngineMenu("cloud")
		appLog.Printf("☁️ 已切换到云端引擎: %s", eng.Name())
	default: // local
		eng, err := asr.NewLocalEngine(d.cfg)
		if err != nil {
			appLog.Printf("❌ 本地引擎不可用: %v", err)
			d.mStatus.SetTitle("❌ 本地模型未安装——见「❓ 使用帮助」")
			d.showLocalGuide()
			if d.currentEngine() == nil {
				d.syncEngineMenu("cloud")
			} else {
				d.syncEngineMenu(engineName(d))
			}
			return
		}
		if d.detector == nil {
			if err := d.ensureVAD(); err != nil {
				appLog.Printf("❌ VAD 模型加载失败: %v", err)
				return
			}
		}
		d.setEngine(eng)
		d.engineReady.Store(true)
		d.syncEngineMenu("local")
		appLog.Printf("🏠 已切换到本地引擎: %s", eng.Name())
	}
	_ = config.PatchConfig(map[string]any{"engine": which})

	// 引擎变化后重建流式引擎(流式模式下会话依赖引擎)
	if d.streamingMode.Load() {
		wasListening := d.listening.Load()
		if wasListening {
			d.setListening(false)
		}
		d.streamEng = nil
		if err := d.initStreaming(); err != nil {
			appLog.Printf("⚠️ 流式引擎重建失败: %v", err)
		}
		if wasListening {
			d.setListening(true)
		}
	}
	d.refreshMenu()
}

// engineName 当前引擎对应的菜单标识(本地→local,云端→cloud,未就绪→local)。
func engineName(d *dictation) string {
	if e := d.currentEngine(); e != nil && strings.Contains(e.Name(), "cloud") {
		return "cloud"
	}
	return "local"
}

// ensureVAD 确保 VAD 断句模型存在(缺失则下载 1.7MB)并初始化 detector。
func (d *dictation) ensureVAD() error {
	if err := setup.EnsureVAD(d.cfg, nil); err != nil {
		return err
	}
	det, err := vad.New(d.cfg)
	if err != nil {
		return err
	}
	d.detector = det
	return nil
}

// showHelp 综合使用指引(菜单「❓ 使用帮助」)。
func (d *dictation) showHelp() {
	lines := []string{
		"【听写】Ctrl+Option+V 或菜单开关;说完停顿半秒,文字打进当前输入框。",
		"",
		"【本地引擎·离线】识别模型 228MB,三选一:",
		"  ① 推荐:菜单点「⬇️ 下载缺失模型」自动下载",
		"  ② 终端执行:voice-input setup",
		"  ③ 分发包用户:双击文件夹内 Install.command",
		"  手动下载:hf-mirror.com 搜 csukuangfj/sherpa-onnx-sense-voice",
		"  安装验证:voice-input check 显示引擎加载成功",
		"",
		"【云端引擎·高质量】菜单「切换引擎」→云端,首次弹窗输入 API Key:",
		"  申请地址:https://bailian.console.aliyun.com",
		"  (开通百炼 → API-KEY 管理 → 创建 Key;账户需有额度)",
	}
	if btn := showDialog("voice-input · 使用帮助", lines, []string{"好"}, "好"); btn == "好" {
		_ = exec.Command("open", "https://github.com/Yx0201/voice-input").Run()
	}
}

// showLocalGuide 切换本地引擎但模型缺失时的安装指引。
func (d *dictation) showLocalGuide() {
	lines := []string{
		"本地识别模型未安装(228MB),三选一:",
		"① 推荐:菜单点「⬇️ 下载缺失模型」自动下载安装",
		"② 分发包用户:双击文件夹内 Install.command",
		"③ 终端执行:voice-input setup",
		"",
		"安装完成后:菜单点「开启听写」即可",
		"(验证:voice-input check 显示引擎加载成功)",
	}
	showDialog("voice-input · 本地模型安装", lines, []string{"好"}, "好")
}

// showCloudGuide 云端 Key 缺失/取消时的申请指引,附一键打开官网。
func (d *dictation) showCloudGuide() {
	lines := []string{
		"云端引擎需要百炼 API Key(sk- 开头):",
		"① 打开 https://bailian.console.aliyun.com",
		"② 开通百炼 → API-KEY 管理 → 创建 Key(账户需有余额)",
		"③ 回到菜单:切换引擎 → 云端,粘贴输入",
	}
	if btn := showDialog("voice-input · 云端引擎配置", lines, []string{"取消", "打开官网"}, "打开官网"); btn == "打开官网" {
		_ = exec.Command("open", "https://bailian.console.aliyun.com").Run()
	}
}

// showDialog 弹系统对话框(多行文本拼接 + 自定义按钮),返回被点击的按钮文案。
func showDialog(title string, lines []string, buttons []string, defaultButton string) string {
	quoted := make([]string, len(lines))
	for i, l := range lines {
		quoted[i] = `"` + strings.ReplaceAll(l, `"`, `'`) + `"`
	}
	body := strings.Join(quoted, " & return & ")
	btnList := make([]string, len(buttons))
	for i, b := range buttons {
		btnList[i] = `"` + b + `"`
	}
	script := fmt.Sprintf(`display dialog %s with title "%s" buttons {%s} default button "%s"`,
		body, title, strings.Join(btnList, ","), defaultButton)

	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		return ""
	}
	s := string(out)
	i := strings.Index(s, "button returned:")
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(s[i+len("button returned:"):])
	return strings.SplitN(rest, ",", 2)[0]
}

// syncEngineMenu 让子菜单勾选状态与实际引擎一致。
func (d *dictation) syncEngineMenu(active string) {
	if d.mEngLocal == nil {
		return
	}
	if active == "cloud" {
		d.mEngCloud.Check()
		d.mEngLocal.Uncheck()
	} else {
		d.mEngLocal.Check()
		d.mEngCloud.Uncheck()
	}
}

// promptAPIKey 弹系统对话框输入百炼 API Key(输入内容隐藏显示)。
// 返回 (key, 是否确认)。
func promptAPIKey() (string, bool) {
	script := `display dialog "输入阿里百炼(DashScope)API Key(控制台 sk- 开头)" with title "voice-input · 云端引擎" default answer "" with hidden answer buttons {"取消", "保存"} default button "保存"`
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		return "", false // 用户点了取消
	}
	s := string(out)
	if !strings.Contains(s, "button returned:保存") {
		return "", false
	}
	i := strings.Index(s, "text returned:")
	if i < 0 {
		return "", false
	}
	key := strings.TrimSpace(s[i+len("text returned:"):])
	if key == "" {
		return "", false
	}
	return key, true
}

// startMic 首次打开麦克风。
func (d *dictation) startMic() error {
	mic, err := capture.NewMic(sampleRate, 1, d.cfg.InputDevice, func(pcm []float32) {
		if !d.listening.Load() {
			return
		}
		select {
		case d.audioCh <- pcm:
		default: // 识别阻塞时丢帧,保采音
		}
	})
	if err != nil {
		return err
	}
	d.mic = mic
	d.micReady.Store(true)
	return nil
}

// hotkeyLoop 热键监督循环:按当前触发方式(toggle/ptt)注册监听;
// 模式切换时热重配(Stop→重注册);注册失败每 5 秒重试。
func (d *dictation) hotkeyLoop() {
	failLogged := false
	for {
		mode, _ := d.hotkeyMode.Load().(string)
		done := make(chan struct{})

		go func(mode string, done chan struct{}) {
			defer close(done)
			var err error
			if mode == "ptt" {
				err = hotkey.ListenPTT(d.cfg.PttModifiers, d.cfg.PttKey,
					func() { d.setListening(true) },  // 按下:开麦
					func() { d.setListening(false) }, // 松开:结束并定稿
					func() {
						appLog.Printf("⌨️  按住说话已生效: %s+%s(松开结束;按键不会打出空格)",
							fmtModifiers(d.cfg.PttModifiers), orDefault(d.cfg.PttKey, "space"))
					})
			} else {
				combo := fmtModifiers(d.cfg.HotkeyModifiers) + "+" + d.cfg.HotkeyKey
				err = hotkey.Listen(d.cfg.HotkeyModifiers, d.cfg.HotkeyKey, d.toggle, func() {
					appLog.Printf("⌨️  热键已生效: %s 切换听写", combo)
				})
			}
			if err != nil && !failLogged {
				appLog.Printf("⌨️  热键注册失败(%v),每 5 秒重试——辅助功能授权后自动生效", err)
				failLogged = true
			}
		}(mode, done)

		select {
		case <-d.hotkeyRestart: // 模式切换:停旧起新
			hotkey.Stop()
			<-done
			failLogged = false
		case <-done: // 注册失败退出,稍后重试
			time.Sleep(5 * time.Second)
		}
	}
}

// setHotkeyMode 菜单切换触发方式(组合键切换 ⇄ 按住说话),热重配热键并持久化。
func (d *dictation) setHotkeyMode(mode string) {
	cur, _ := d.hotkeyMode.Load().(string)
	if mode == cur {
		d.syncTrigMenu()
		return
	}
	// 切到按住说话:先停常开听写,保持静默直到用户按键
	if mode == "ptt" && d.listening.Load() {
		d.setListening(false)
	}
	d.hotkeyMode.Store(mode)
	select {
	case d.hotkeyRestart <- struct{}{}:
	default:
	}
	d.syncTrigMenu()
	_ = config.PatchConfig(map[string]any{"hotkey_mode": mode})
	if mode == "ptt" {
		d.mStatus.SetTitle("🎙 按住 Option+空格 说话")
		appLog.Printf("⌨️  触发方式 → 按住说话(%s+%s)", fmtModifiers(d.cfg.PttModifiers), d.cfg.PttKey)
	} else {
		appLog.Printf("⌨️  触发方式 → 组合键切换(%s+%s)", fmtModifiers(d.cfg.HotkeyModifiers), d.cfg.HotkeyKey)
	}
}

// syncTrigMenu 触发方式子菜单勾选与实际一致。
func (d *dictation) syncTrigMenu() {
	if d.mTrigPtt == nil {
		return
	}
	if d.hotkeyMode.Load() == "ptt" {
		d.mTrigPtt.Check()
		d.mTrigToggle.Uncheck()
	} else {
		d.mTrigToggle.Check()
		d.mTrigPtt.Uncheck()
	}
}

// toggle 切换听写状态。
func (d *dictation) toggle() {
	if d.listening.Load() {
		d.setListening(false)
	} else {
		d.setListening(true)
	}
}

// setListening 设置状态并同步麦克风占用与菜单显示。
// 幂等:重复开/关直接返回(按住说话的按键自动重复依赖此保护)。
func (d *dictation) setListening(on bool) {
	if on && d.listening.Load() {
		return
	}
	if !on && !d.listening.Load() {
		return
	}
	if on {
		if d.streamingMode.Load() {
			d.streamMu.Lock()
			if d.streamEng == nil {
				if err := d.initStreaming(); err != nil {
					d.streamMu.Unlock()
					appLog.Printf("❌ 流式引擎不可用: %v", err)
					d.mStatus.SetTitle("❌ 流式引擎:" + err.Error())
					return
				}
			}
			if d.streamSession == nil {
				sess, err := d.streamEng.NewSession()
				if err != nil {
					d.streamMu.Unlock()
					appLog.Printf("❌ 创建流式会话失败: %v", err)
					d.mStatus.SetTitle("❌ 流式会话:" + err.Error())
					return
				}
				d.streamSession = sess
				d.committed, d.lastPartial = "", ""
			}
			d.streamMu.Unlock()
		} else if d.currentEngine() == nil || d.detector == nil {
			d.mStatus.SetTitle("⚠️ 引擎未就绪——本地模型或云端 Key 需其一(见「❓ 使用帮助」)")
			appLog.Printf("⚠️ 无法开启听写:请下载本地模型(菜单)或切换云端输入 API Key")
			return
		}
		if !d.micReady.Load() {
			if err := d.startMic(); err != nil {
				appLog.Printf("❌ 打开麦克风失败: %v", err)
				d.mStatus.SetTitle("❌ 麦克风不可用:请检查 系统设置→隐私与安全性→麦克风")
				return
			}
			appLog.Printf("🎤 麦克风已打开")
		}
		// 丢弃暂停期间残留的音频与半句语音
		for len(d.audioCh) > 0 {
			<-d.audioCh
		}
		// 整句模式才需要 VAD 断句;流式模式下 detector 为 nil,不得触碰
		if !d.streamingMode.Load() && d.detector != nil {
			d.detector.Reset()
		}
		if err := d.mic.Start(); err != nil {
			appLog.Printf("❌ 恢复麦克风失败: %v", err)
			return
		}
		d.listening.Store(true)
		appLog.Printf("🎤 听写已开启")
	} else {
		d.listening.Store(false)

		// 松开/暂停:先关麦(状态栏橙点立即熄灭),收尾全部异步——
		// 在途识别与打字不被阻断(按住说话松手后文字仍会补完)
		if d.micReady.Load() {
			if err := d.mic.Stop(); err != nil {
				appLog.Printf("⚠️ 暂停麦克风失败: %v", err)
			}
		}

		if d.streamingMode.Load() {
			// 流式:立即换新会话位,旧会话后台收尾(等云端吐完最后结果)
			d.streamMu.Lock()
			sess := d.streamSession
			startTyped := d.committed
			d.streamSession = nil
			d.committed, d.lastPartial = "", ""
			d.streamMu.Unlock()
			if sess != nil {
				go d.drainSession(sess, startTyped)
			}
		} else if d.detector != nil {
			// 整句:补静音强制收割尾段,转写异步进行
			if eng := d.currentEngine(); eng != nil {
				for _, seg := range d.detector.Flush() {
					go transcribeAndType(eng, seg)
				}
			}
		}
		appLog.Printf("⏸  听写已暂停(在途识别继续)")
	}
	d.refreshMenu()
}

// drainSession 后台收尾一个流式会话:轮询 partial/endpoint,把剩余文字补打进
// 输入框后关闭会话。startTyped 为松手前已打进的部分,只补增量。
func (d *dictation) drainSession(sess asr.StreamingSession, startTyped string) {
	if sess == nil {
		return
	}
	defer func() {
		_ = sess.Close()
		appLog.Printf("🎙 会话收尾完成")
	}()
	typed := startTyped
	granted := inject.IsAccessibilityGranted()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if sess.Endpoint() {
			final := strings.TrimSpace(sess.Finalize())
			if final != "" && granted {
				if strings.HasPrefix(final, typed) {
					if delta := final[len(typed):]; delta != "" {
						inject.TypeText(delta)
					}
				} else if typed == "" {
					inject.TypeText(final)
				} else {
					appLog.Printf("⚠️ 定稿与已提交不一致(保留已提交):已=%q 定=%q", typed, final)
				}
			}
			return
		}
		if p := sess.Partial(); granted && strings.HasPrefix(p, typed) && len(p) > len(typed) {
			inject.TypeText(p[len(typed):])
			typed = p
		}
		time.Sleep(150 * time.Millisecond)
	}
	appLog.Printf("⚠️ 会话收尾超时(8s),丢弃未返回的尾部")
}

// refreshMenu 更新菜单栏文案。
func (d *dictation) refreshMenu() {
	if d.mStatus == nil {
		return
	}
	if !d.engineReady.Load() {
		return // 错误/下载状态由对应分支维护
	}
	if d.listening.Load() {
		d.mStatus.SetTitle("听写:开启中")
		d.mToggle.SetTitle("暂停听写")
	} else {
		d.mStatus.SetTitle("听写:已暂停")
		d.mToggle.SetTitle("开启听写")
	}
}

// audioLoop 消费音频,切段转写并注入;每 2s 记录一次输入电平(诊断采音问题)。
func (d *dictation) audioLoop() {
	var sumSq float64
	var count int
	lastReport := time.Now()

	for pcm := range d.audioCh {
		for _, s := range pcm {
			sumSq += float64(s) * float64(s)
		}
		count += len(pcm)

		if time.Since(lastReport) >= 2*time.Second {
			if count > 0 {
				rms := sumSq / float64(count)
				db := 20 * log10(rms)
				appLog.Printf("📞 输入电平 %.1f dBFS%s", db, map[bool]string{true: "(有声)", false: "(静音)"}[db > -55])
			}
			sumSq, count = 0, 0
			lastReport = time.Now()
		}

		if d.streamingMode.Load() {
			d.streamFeed(pcm)
			continue
		}
		eng := d.currentEngine()
		if eng == nil || d.detector == nil {
			continue
		}
		for _, seg := range d.detector.Feed(pcm) {
			transcribeAndType(eng, seg)
		}
	}
}

// commonPrefix 两个字符串的最长公共前缀(按 rune,避免切在多字节字符中间)。
func commonPrefix(a, b string) string {
	ar, br := []rune(a), []rune(b)
	n := len(ar)
	if len(br) < n {
		n = len(br)
	}
	i := 0
	for i < n && ar[i] == br[i] {
		i++
	}
	return string(ar[:i])
}

// orDefault 空字符串回退。
func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func log10(x float64) float64 { return math.Log10(x) }

func fmtModifiers(mods []string) string {
	out := ""
	for i, m := range mods {
		if i > 0 {
			out += "+"
		}
		out += m
	}
	return out
}
