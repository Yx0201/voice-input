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
	"context"
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
	"unicode/utf8"

	"fyne.io/systray"

	"voice_input/internal/asr"
	"voice_input/internal/capsule"
	"voice_input/internal/capture"
	"voice_input/internal/config"
	"voice_input/internal/hotkey"
	"voice_input/internal/inject"
	"voice_input/internal/keystore"
	"voice_input/internal/polish"
	"voice_input/internal/setup"
	"voice_input/internal/vad"
)

// appLog 双击运行时 stdout 不可见,所有输出同步落到日志文件。
var appLog *log.Logger

// ---- 全局串行化执行线(时序问题的根治)----
// 文字注入与整句转写都必须离开采音/音频处理执行线,否则任何一环慢
// (云端 1-3s 转写、网络回压、大段打字)都会冻结波形甚至丢音频。
// 注入与分段各用一条单消费者队列:顺序有保证,生产者永不阻塞在慢操作上。

// typeCh 打字队列:所有 inject.TypeText 唯一入口,FIFO 保证文字顺序。
var typeCh = make(chan string, 256)

// 撤销打字单元:单元 = **一次润色输出批次**(2026-09-25 用户以具体场景拍板:
// 按住期间润色分几段输出就记几段,撤销一段一段往回删;一次按住只说一句短话
// 时,那一段就是完整内容)。串行化修复后,输入框每出现一段文字必然对应
// 恰好一次润色输出,批次即天然单元。
var (
	injMu      sync.Mutex
	typedUnits []typedUnit
	lastUndoAt time.Time
)

type typedUnit struct {
	text string
	at   time.Time
}

const (
	maxTypedUnits  = 50
	undoDebounceMs = 400 // 热键防抖(叠加 tap 层的自动重复过滤,双保险)
)

// axDropped 辅助功能失效时的节流标记:失效episode只记一行,不刷屏。
var axDropped atomic.Bool

// 撤销哨兵消息:撤销必须与打字同队列串行执行,
// 否则退格会与在途文字交错(按撤销时队列里可能还有未落地的字)。
const undoSentinel = "\x00undo"

func typeLoop() {
	for s := range typeCh {
		if s == undoSentinel {
			doUndo()
			continue
		}
		// 静默跳过是远程排障黑洞:用户"没有任何文字出现"而日志一片空白。
		// 每个失效episode只记一行(含被丢弃文本),恢复后重置。
		if !inject.IsAccessibilityGranted() {
			if axDropped.CompareAndSwap(false, true) {
				log.Printf("🚫 辅助功能权限失效,文字无法注入(丢弃:%q)——去 系统设置→隐私与安全性→辅助功能 重新开启", s)
			} else {
				log.Printf("🚫 辅助功能权限失效,丢弃:%q", s)
			}
			continue
		}
		axDropped.Store(false)
		injMu.Lock()
		typedUnits = append(typedUnits, typedUnit{s, time.Now()})
		if len(typedUnits) > maxTypedUnits {
			typedUnits = typedUnits[1:]
		}
		injMu.Unlock()
		inject.TypeText(s)
	}
}

// requestUndo 请求撤销上一句(菜单/热键调用;排队进打字线串行执行)。
// 400ms 防抖:挡住手抖连击与系统按键重复(tap 层已滤自动重复,双保险)。
func requestUndo() {
	if time.Since(lastUndoAt) < undoDebounceMs*time.Millisecond {
		return
	}
	lastUndoAt = time.Now()
	select {
	case typeCh <- undoSentinel:
	default:
		log.Printf("⚠️ 打字队列满,撤销请求丢弃")
	}
}

// doUndo 在打字执行线上删除最后一个单元(此处必然无在途文字)。
func doUndo() {
	injMu.Lock()
	n := len(typedUnits)
	if n == 0 {
		injMu.Unlock()
		appLog.Printf("↩️ 撤销:没有可撤销的听写内容")
		return
	}
	unit := typedUnits[n-1]
	typedUnits = typedUnits[:n-1]
	injMu.Unlock()
	if !inject.IsAccessibilityGranted() {
		appLog.Printf("↩️ 撤销失败:辅助功能权限未生效")
		return
	}
	count := len([]rune(unit.text))
	appLog.Printf("↩️ 撤销上一句(%d 字):%q", count, unit.text)
	inject.Backspaces(count)
}

// typeTextAsync 把一段文字排队打进焦点输入框(队列满时丢弃并记日志,理论上不会发生)。
func typeTextAsync(s string) {
	select {
	case typeCh <- s:
	default:
		log.Printf("⚠️ 注入队列满,丢弃:%q", s)
	}
}

// segJob 一段待转写的音频(携带引擎快照,菜单热切换不影响在途任务;d 供润色判断,CLI 下为 nil)。
type segJob struct {
	d   *dictation
	eng asr.Engine
	seg []float32
}

// segCh 整句模式分段队列:转写(含云端数秒延迟)不阻塞音频线,分段保序。
var segCh = make(chan segJob, 64)

// segLoop 单消费者转写执行线。
func segLoop() {
	for j := range segCh {
		transcribeAndType(j.d, j.eng, j.seg)
	}
}

// polishJob 一次待输出任务:短句(<minChars 且非 force)在消费者内部直出,
// 其余送 LLM。统一走单队列是顺序保证的关键——曾经短句绕过队列直接打字,
// 超车了还在 LLM 里泡着的先到批次,用户看到文字顺序与说话顺序颠倒
// (2026-09-25 实测事故)。
type polishJob struct {
	text     string
	force    bool // 松手/端点冲刷:短句也照常判定(仍可直出,但保序)
	minChars int
}

// polishCh 输出任务队列:原始识别文本进,润色(或短句直出)后打字出。
// 单消费者保证文字顺序;调用方永不阻塞在 LLM 往返上。
var polishCh = make(chan polishJob, 8)

// polishLoop 单消费者输出执行线:短句直出 / 云端清理 → 打字;
// 失败/异常静默回退原文(日志仅留一行排障痕迹)。在途计入胶囊转换态
// (松手后的末次冲刷期间转圈不提前消失)。
func polishLoop(cfgFun func() polish.Config) {
	for job := range polishCh {
		raw := job.text
		conv := capsule.BeginConvert()
		if !job.force && job.minChars > 0 && utf8.RuneCountInString(raw) < job.minChars {
			appLog.Printf("↩️ 短句跳过润色直出:%q", raw)
			typeTextAsync(raw)
			conv()
			continue
		}
		out, err := polish.Clean(context.Background(), cfgFun(), raw)
		if err == nil && !polish.SanityOK(raw, out) {
			err = fmt.Errorf("输出异常缩短(%d→%d 字)", utf8.RuneCountInString(raw), utf8.RuneCountInString(out))
		}
		if err != nil {
			appLog.Printf("✨润色不可用,已回退原文(%v)", err)
			typeTextAsync(raw)
		} else {
			out = ensureEndPunct(out)
			appLog.Printf("✨ 润色:%q → %q", raw, out)
			typeTextAsync(out)
		}
		conv()
	}
}

// feedLevel 在采音线程就地计算一帧的 RMS 并喂给声音胶囊。
// 与识别/注入完全解耦:识别卡多久,波形都照常起伏。
func feedLevel(pcm []float32) {
	if len(pcm) == 0 {
		return
	}
	var sq float64
	for _, s := range pcm {
		v := float64(s)
		sq += v * v
	}
	capsule.Level(math.Sqrt(sq/float64(len(pcm))) * 9) // 语音典型 0.02~0.15,增益 9 拉满
}

// buildStamp 构建时由 Makefile 注入 git 短 hash(-ldflags -X)。
// 远程排障第一线索:用户发来的日志首行即定位其运行的确切代码版本。
var buildStamp = "dev"

// maxLogSize 日志轮转阈值:超过则 app.log → app.log.old(只留一代)。
// 分发用户长期使用日志不能无限膨胀;"发日志给你"时也才有可发送的体积。
const maxLogSize = 2 << 20 // 2MB

func setupAppLog() {
	dir := config.DefaultDir()
	_ = os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "app.log")
	if st, err := os.Stat(path); err == nil && st.Size() > maxLogSize {
		_ = os.Rename(path, filepath.Join(dir, "app.log.old"))
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
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
	cfg         config.Config
	engine      asr.Engine // 整句引擎;engineMu 保护(菜单可热切换)
	engineMu    sync.RWMutex
	detector    *vad.Detector
	mic         *capture.Mic
	audioCh     chan []float32
	listening   atomic.Bool
	micReady    atomic.Bool
	axRequested atomic.Bool
	engineReady atomic.Bool
	downloading atomic.Bool

	// 流式听写(即时出字)
	streamingMode atomic.Bool
	streamEng     asr.StreamingEngine // 当前引擎对应的流式实现(懒加载)
	streamSession asr.StreamingSession
	streamMu      sync.Mutex // 保护 session/committed/lastPartial
	committed     string     // 已打进输入框的稳定前缀
	lastPartial   string     // 上一次 partial(两次一致视为稳定)

	// 文字润色(开启时:识别文本先进缓冲,攒到边界经 LLM 清理再打字)
	polishMu  sync.Mutex // 保护 polishBuf
	polishBuf string     // 待润色的原始识别文本缓冲

	mStatus       *systray.MenuItem
	mToggle       *systray.MenuItem
	mDownload     *systray.MenuItem
	mEngLocal     *systray.MenuItem
	mEngCloud     *systray.MenuItem
	mModeStream   *systray.MenuItem
	mModeSentence *systray.MenuItem
	mTrigToggle   *systray.MenuItem
	mTrigPtt      *systray.MenuItem
	mPolish       *systray.MenuItem // 文字润色(云端),勾选即生效

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

// ---- 文字润色管线 ----
// 仅云端单通道(默认开启,2026-09-25 裁决)。识别文本不直接打字:appendText 进
// 缓冲,攒到边界(句末标点且≥min / 超 hard max / 端点定稿 / 松手收尾)冲刷给
// polishLoop,云端 LLM 清理后打字。优雅降级:未配置百炼 Key 时静默失效(输出
// 原文),请求失败/超时同样静默回退原文——润色永不丢字、不弹窗、不打断听写。

// polishOn 润色是否实际生效:用户未关 + 百炼 Key 在手(无 Key 静默失效)。
// 只读内存 cfg,不做钥匙串 IO(热路径;Key 的补读只在启动/切换时做一次)。
func (d *dictation) polishOn() bool {
	return d.cfg.PolishProvider == "bailian" && d.cfg.DashScopeAPIKey != ""
}

// polishCfg 装配润色通道配置。
func (d *dictation) polishCfg() polish.Config {
	return polish.Config{
		Model:          d.cfg.PolishModel,
		BailianAPIKey:  d.cfg.DashScopeAPIKey,
		BailianBaseURL: bailianBaseURL(d.cfg),
		Timeout:        time.Duration(d.cfg.PolishTimeoutMs) * time.Millisecond,
	}
}

// bailianBaseURL 业务空间端点(有 workspace id 时),否则公共兼容模式。
func bailianBaseURL(cfg config.Config) string {
	if cfg.DashScopeWorkspaceID != "" {
		return fmt.Sprintf("https://%s.%s.maas.aliyuncs.com/compatible-mode/v1",
			cfg.DashScopeWorkspaceID, orDefault(cfg.Region, "cn-beijing"))
	}
	return ""
}

// appendText 识别文本唯一入口:润色关 = 直接打字;开 = 进缓冲并评估冲刷。
func (d *dictation) appendText(s string) {
	if s == "" {
		return
	}
	d.polishMu.Lock()
	if !d.polishOn() {
		d.polishMu.Unlock()
		typeTextAsync(s)
		return
	}
	d.polishBuf += s
	n := utf8.RuneCountInString(d.polishBuf)
	max := d.cfg.PolishMaxChars
	if max <= 0 {
		max = 120
	}
	min := d.cfg.PolishMinChars
	if min <= 0 {
		min = 20
	}
	flush := n >= max ||
		(n >= min && endsWithSentencePunct(d.polishBuf))
	if !flush {
		d.polishMu.Unlock()
		return
	}
	raw := d.polishBuf
	d.polishBuf = ""
	d.polishMu.Unlock()
	select {
	case polishCh <- polishJob{raw, false, min}:
	default: // 队列满(理论上不可能):塞回缓冲,下轮再冲
		d.polishMu.Lock()
		d.polishBuf = raw + d.polishBuf
		d.polishMu.Unlock()
	}
}

// polishFlush 冲刷缓冲去润色。force=false 时短于 min 的内容原样直出
// (短句没有口水词可去,白等一次 LLM);force=true 一律走润色。
func (d *dictation) polishFlush(force bool) {
	d.polishMu.Lock()
	raw := d.polishBuf
	d.polishBuf = ""
	d.polishMu.Unlock()
	if raw == "" {
		return
	}
	min := d.cfg.PolishMinChars
	if min <= 0 {
		min = 20
	}
	select {
	case polishCh <- polishJob{raw, force, min}:
	default:
		typeTextAsync(raw) // 队列满也不丢字(极端情况,保序代价可接受)
	}
}

// polishReset 清空缓冲(开新听写轮时;内容属上一轮,不冲刷)。
func (d *dictation) polishReset() {
	d.polishMu.Lock()
	raw := d.polishBuf
	d.polishBuf = ""
	d.polishMu.Unlock()
	// 快速连按场景:松手后 drainSession 还在往缓冲补尾巴,此刻重新按住
	// 不能把残留丢弃——原文直出(不润色陈旧内容),一个字都不许少。
	if raw != "" {
		appLog.Printf("↩️ 上轮残留缓冲入队:%q", raw)
		select {
		case polishCh <- polishJob{raw, true, 0}:
		default:
			typeTextAsync(raw)
		}
	}
}

// runApp 启动菜单栏应用;阻塞直至退出。
func runApp() {
	setupAppLog()
	appLog.Printf("== voice-input 启动(版本 0.1.0+%s)==", buildStamp)

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
	go typeLoop()
	go segLoop()
	go polishLoop(func() polish.Config { return d.polishCfg() })
	// 撤销上一句热键(附加热键,slot1;须在 hotkeyLoop 首次建 tap 前注册)
	if err := hotkey.SetExtra(cfg.UndoModifiers, orDefault(cfg.UndoKey, "z"), requestUndo); err != nil {
		appLog.Printf("⚠️ 撤销热键注册失败(%v),菜单仍可撤销", err)
	} else {
		appLog.Printf("↩️ 撤销热键: %s+%s", fmtModifiers(cfg.UndoModifiers), orDefault(cfg.UndoKey, "z"))
	}
	// 润色默认开启(云端):Key 只在配置/钥匙串里时补读一次,无 Key 静默失效
	if d.cfg.PolishProvider != "off" && d.cfg.DashScopeAPIKey == "" {
		if k := keystore.Load(); k != "" {
			d.cfg.DashScopeAPIKey = k
			appLog.Printf("✨ 文字润色已就绪(云端,Key 取自钥匙串)")
		} else {
			appLog.Printf("✨ 文字润色静默关闭:未配置百炼 API Key")
		}
	}
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

// streamPuncts 流式提交与润色冲刷共用的句读标点集。
var streamPuncts = "。?!;;、,.!?"

// ensureEndPunct 批次输出保住句尾标点:LLM 偶发吃掉末尾句号,两个批次拼起来
// 句子边界消失,读起来像顺序错乱(2026-09-25 实测"只回退我们+刚才记录的…")。
// 润色批次以句读边界冲刷,末尾补句号忠实还原语义。
func ensureEndPunct(out string) string {
	if out == "" || endsWithSentencePunct(out) {
		return out
	}
	return out + "。"
}

// endsWithSentencePunct 文本是否以任一句读标点结尾(润色冲刷的边界信号)。
func endsWithSentencePunct(s string) bool {
	r := []rune(s)
	return len(r) > 0 && strings.ContainsRune(streamPuncts, r[len(r)-1])
}

// streamFeed 流式模式处理一帧音频。
func (d *dictation) streamFeed(pcm []float32) {
	d.streamMu.Lock()
	defer d.streamMu.Unlock()

	if d.streamSession == nil {
		return // 会话在 setListening(true) 创建;此处防御
	}
	d.streamSession.Feed(pcm, sampleRate)
	d.commitStable(d.streamSession.Partial(), d.streamSession.PartialReliable())

	if d.streamSession.Endpoint() {
		final := d.streamSession.Finalize()
		d.commitExact(final)
		d.committed, d.lastPartial = "", ""
		d.polishFlush(false) // 端点定稿:缓冲立即出(短句自动跳过润色直出)
		if final != "" {
			appLog.Printf("🎙 定稿:%s", final)
		}
	}
}

// lastPunctCut 返回 partial 中最后一个句读标点(含)为止的稳定前缀。
// 必须**按 rune**切:中文标点"。、;"是 3 字节,字节级 partial[:i+1] 会拦腰
// 切断产生非法 UTF-8——2026-09-25 实锤事故:烂字节既打出不可见乱码,又让
// 已提交文本永远无法与云端定稿前缀/锚点匹配,整条定稿被丢弃(吞一整段)。
func lastPunctCut(partial string) (string, bool) {
	pr := []rune(partial)
	for i := len(pr) - 1; i >= 0; i-- {
		if strings.ContainsRune(streamPuncts, pr[i]) {
			return string(pr[:i+1]), true
		}
	}
	return "", false
}

// commitStable 计算 partial 的稳定前缀并增量注入(永不回删)。
// 稳定判定:① 最后一个句读标点之前(含);② 连续两次 partial 公共前缀(≥2 新字);
// ③ 兜底:未提交部分超过 6 字时整段提交(防无标点长句迟迟不出字)。
// reliable=false(云端 partial 会回改早前文字)时只用规则①:预览提前提交
// 未定稿文字会被服务端重写,已提交前缀一旦失配,打字守卫会永久卡死。
func (d *dictation) commitStable(partial string, reliable bool) {
	if partial == "" {
		return
	}
	stable, _ := lastPunctCut(partial)
	if reliable {
		if stable == "" {
			if cp := commonPrefix(d.lastPartial, partial); len([]rune(cp))-len([]rune(d.committed)) >= 2 {
				stable = cp
			}
		}
		if stable == "" && len([]rune(partial))-len([]rune(d.committed)) >= 6 {
			stable = partial
		}
	}
	d.lastPartial = partial

	if len(stable) > len(d.committed) && strings.HasPrefix(stable, d.committed) {
		delta := stable[len(d.committed):]
		if delta != "" && inject.IsAccessibilityGranted() {
			d.appendText(delta) // 润色开=进缓冲攒批;关=直接打字(顺序均保)
			d.committed = stable
		}
	}
}

// commitExact 端点定稿:只追加不回删;前缀失配(云端回改标点/同音字)时按
// 锚点补救尾巴,无法对齐才放弃并记日志。
func (d *dictation) commitExact(final string) {
	final = strings.TrimSpace(final)
	if final == "" {
		return
	}
	if strings.HasPrefix(final, d.committed) {
		if delta := final[len(d.committed):]; delta != "" && inject.IsAccessibilityGranted() {
			d.appendText(delta)
		}
	} else if d.committed == "" {
		if inject.IsAccessibilityGranted() {
			d.appendText(final)
		}
	} else if tail, ok := salvageTail(d.committed, final); ok {
		// 补救尾巴若只是重复的句读标点(云端把上轮已打的句尾收编进定稿),
		// 不再打进输入框——用户看到的"句首一个。"就是它(2026-09-25 案例)
		if strings.Trim(tail, streamPuncts+" ") != "" && inject.IsAccessibilityGranted() {
			appLog.Printf("🔧 定稿尾部补救(云端回改致前缀失配):+%q", tail)
			d.appendText(tail)
		}
	} else {
		appLog.Printf("⚠️ 定稿与已提交不一致(无法对齐,保留已提交):已=%q 定=%q", d.committed, final)
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

	d.mStatus = systray.AddMenuItem("启动中……", "")
	d.mStatus.Disable()
	d.mToggle = systray.AddMenuItem("暂停听写", "")
	mUndo := systray.AddMenuItem("↩️ 撤销上一句", "")
	d.mDownload = systray.AddMenuItem("⬇️ 下载缺失模型(约 230MB)", "")

	// 引擎切换子菜单(本地/云端二选一)
	mEng := systray.AddMenuItem("切换引擎", "")
	d.mEngLocal = mEng.AddSubMenuItemCheckbox("本地(SenseVoice·离线)", "", d.cfg.Engine != "cloud")
	d.mEngCloud = mEng.AddSubMenuItemCheckbox("云端(百炼·高质量)", "", d.cfg.Engine == "cloud")

	// 听写模式子菜单(即时出字/完整句)
	mMode := systray.AddMenuItem("听写模式", "")
	d.mModeStream = mMode.AddSubMenuItemCheckbox("即时出字(流式)", "", d.streamingMode.Load())
	d.mModeSentence = mMode.AddSubMenuItemCheckbox("完整句(更准)", "", !d.streamingMode.Load())

	// 触发方式子菜单(组合键切换/按住说话)
	ptt := d.hotkeyMode.Load() == "ptt"
	mTrig := systray.AddMenuItem("触发方式", "")
	d.mTrigToggle = mTrig.AddSubMenuItemCheckbox("组合键切换(Ctrl+Option+V)", "", !ptt)
	d.mTrigPtt = mTrig.AddSubMenuItemCheckbox("按住说话(Option+空格)", "", ptt)

	// 文字润色(云端,默认开启):无 Key 时静默失效,勾选状态反映实际可用性
	d.mPolish = systray.AddMenuItemCheckbox("文字润色(云端·去口水词)", "",
		d.polishOn())

	mAX := systray.AddMenuItem("请求辅助功能授权…", "")
	mHelp := systray.AddMenuItem("❓ 使用帮助", "")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出", "")

	go func() {
		for {
			select {
			case <-d.mToggle.ClickedCh:
				d.toggle()
			case <-mUndo.ClickedCh:
				requestUndo()
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
			case <-d.mPolish.ClickedCh:
				go d.togglePolish()
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
			d.mStatus.Hide() // ptt 待机态:无状态可示,整行隐藏
			d.mToggle.SetTitle("开启听写(手动)")
		} else {
			go d.enableListening()
		}
	} else {
		d.setStatus("⚠️ 未配置引擎——见「❓ 使用帮助」")
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
	d.setStatus("⬇️ 下载模型中……")

	err := setup.EnsureModels(d.cfg, func(file string, done, total int64) {
		if total > 0 {
			pct := done * 100 / total
			d.setStatus(fmt.Sprintf("⬇️ %s %d%%", file, pct))
		} else {
			d.setStatus(fmt.Sprintf("⬇️ %s %.1fMB", file, float64(done)/1e6))
		}
	})
	if err != nil {
		appLog.Printf("❌ 模型下载失败: %v", err)
		d.setStatus("❌ 下载失败——检查网络后重试(日志见 ~/.voice_input/app.log)")
		return
	}

	if err := d.initEngine(); err != nil {
		appLog.Printf("❌ 模型下载完成但引擎加载失败: %v", err)
		d.setStatus("❌ 引擎加载失败(日志见 ~/.voice_input/app.log)")
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
			d.setStatus("❌ 云端引擎:" + err.Error())
			d.syncEngineMenu(engineName(d))
			return
		}
		// 云端也需要 VAD 断句模型(仅 1.7MB),缺失则自动补下,无需 228MB 主模型
		if d.detector == nil {
			if err := d.ensureVAD(); err != nil {
				appLog.Printf("❌ VAD 模型下载失败: %v", err)
				d.setStatus("❌ 断句模型下载失败,检查网络后重试")
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
			d.setStatus("❌ 本地模型未安装——见「❓ 使用帮助」")
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
	// 内存必须同步更新:下方 initStreaming 按 cfg.Engine 重建流式引擎,
	// 只写文件不更新内存会让流式引擎滞后一次切换(菜单"本地"实际跑云端)。
	d.cfg.Engine = which
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
		feedLevel(pcm) // 波形电平:采音线程就地投递,与识别/注入互不阻塞
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
		d.mStatus.Hide() // ptt 待机态:无状态可示,整行隐藏
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

// togglePolish 菜单勾切换润色。开启条件=百炼 Key 在手;无 Key 静默保持关闭
// (不弹窗,勾选框维持未勾,日志留痕)。关闭是用户显式选择,持久化。
func (d *dictation) togglePolish() {
	if d.cfg.PolishProvider != "off" {
		d.cfg.PolishProvider = "off"
		appLog.Printf("✨ 文字润色已关闭(原始输出)")
	} else {
		if d.cfg.DashScopeAPIKey == "" {
			if k := keystore.Load(); k != "" {
				d.cfg.DashScopeAPIKey = k
			}
		}
		if d.cfg.DashScopeAPIKey == "" {
			appLog.Printf("✨ 润色保持关闭:未配置百炼 API Key(菜单→切换引擎→云端 可配置)")
			d.syncPolishMenu()
			return
		}
		d.cfg.PolishProvider = "bailian"
		appLog.Printf("✨ 文字润色已开启(云端 %s)", orDefault(d.cfg.PolishModel, polish.DefaultBailianModel))
	}
	d.syncPolishMenu()
	_ = config.PatchConfig(map[string]any{"polish_provider": d.cfg.PolishProvider})
}

// syncPolishMenu 勾选状态与实际生效情况一致(无 Key 时即使配置开着也不勾)。
func (d *dictation) syncPolishMenu() {
	if d.mPolish == nil {
		return
	}
	if d.polishOn() {
		d.mPolish.Check()
	} else {
		d.mPolish.Uncheck()
	}
}

// engineLabel 当前引擎的短标签(日志用):本地/云端(带引擎名)。
func engineLabel(d *dictation) string {
	if d.streamingMode.Load() {
		if d.streamEng == nil {
			return "流式未就绪"
		}
		return d.streamEng.Name()
	}
	if e := d.currentEngine(); e != nil {
		return e.Name()
	}
	return "引擎未就绪"
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
					d.setStatus("❌ 流式引擎:" + err.Error())
					return
				}
			}
			if d.streamSession == nil {
				sess, err := d.streamEng.NewSession()
				if err != nil {
					d.streamMu.Unlock()
					appLog.Printf("❌ 创建流式会话失败: %v", err)
					d.setStatus("❌ 流式会话:" + err.Error())
					return
				}
				d.streamSession = sess
				d.committed, d.lastPartial = "", ""
			}
			d.streamMu.Unlock()
		} else if d.currentEngine() == nil || d.detector == nil {
			d.setStatus("⚠️ 引擎未就绪——本地模型或云端 Key 需其一(见「❓ 使用帮助」)")
			appLog.Printf("⚠️ 无法开启听写:请下载本地模型(菜单)或切换云端输入 API Key")
			return
		}
		if !d.micReady.Load() {
			if err := d.startMic(); err != nil {
				appLog.Printf("❌ 打开麦克风失败: %v", err)
				d.setStatus("❌ 麦克风不可用:请检查 系统设置→隐私与安全性→麦克风")
				return
			}
			appLog.Printf("🎤 麦克风已打开")
		}
		// 丢弃暂停期间残留的音频与半句语音,润色缓冲同理(属上一轮的内容)
		for len(d.audioCh) > 0 {
			<-d.audioCh
		}
		d.polishReset()
		// 整句模式才需要 VAD 断句;流式模式下 detector 为 nil,不得触碰
		if !d.streamingMode.Load() && d.detector != nil {
			d.detector.Reset()
		}
		if err := d.mic.Start(); err != nil {
			appLog.Printf("❌ 恢复麦克风失败: %v", err)
			return
		}
		d.listening.Store(true)
		capsule.Begin() // 胶囊出现,显示波形
		appLog.Printf("🎤 听写已开启(%s|%s|%s)",
			map[bool]string{true: "流式", false: "整句"}[d.streamingMode.Load()],
			engineLabel(d), map[bool]string{true: "润色开", false: "润色关"}[d.polishOn()])
	} else {
		d.listening.Store(false)

		// 松开/暂停:先关麦(状态栏橙点立即熄灭),收尾全部异步——
		// 在途识别与打字不被阻断(按住说话松手后文字仍会补完)
		if d.micReady.Load() {
			if err := d.mic.Stop(); err != nil {
				appLog.Printf("⚠️ 暂停麦克风失败: %v", err)
			}
		}

		// 时序约定:先占住在途计数(BeginConvert)再 End——
		// End 的 evaluate 看到 converts>0 必然亮 loading 圈,
		// 消除"先关麦、转换任务稍后才起"导致的胶囊直接消失/波形卡死竞态。
		if d.streamingMode.Load() {
			// 流式:立即换新会话位,旧会话后台收尾(等云端吐完最后结果)
			d.streamMu.Lock()
			sess := d.streamSession
			startTyped := d.committed
			d.streamSession = nil
			d.committed, d.lastPartial = "", ""
			d.streamMu.Unlock()
			if sess != nil {
				conv := capsule.BeginConvert()
				go func() {
					defer conv()
					d.drainSession(sess, startTyped)
				}()
			}
		} else if d.detector != nil {
			// 整句:补静音强制收割尾段;尾段与实时段走同一条转写队列,保序
			if eng := d.currentEngine(); eng != nil {
				conv := capsule.BeginConvert()
				go func() {
					defer conv()
					for _, seg := range d.detector.Flush() {
						segCh <- segJob{d, eng, seg}
					}
				}()
			}
		}
		d.polishFlush(false) // 缓冲里可能还压着未触发边界的文本,松手即出
		capsule.End()        // 无在途计数时才走延迟隐藏(500ms)
		appLog.Printf("⏸  听写已暂停(在途识别继续)")
	}
	d.refreshMenu()
}

// drainSession 后台收尾一个流式会话:轮询 partial/endpoint,把剩余文字补打进
// 输入框后关闭会话。startTyped 为松手前已打进的部分,只补增量。
// 在途计数由调用方(setListening 的松开分支)持有,保证"松手→亮圈"时序确定。
// 打字走 typeCh 全局队列,与实时提交保持顺序。
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
	// 声明音频结束:本地流补尾垫静音冲刷解码,端点检测才能触发定稿;
	// 不调的话 Endpoint 永不为真,只能干等 8s 超时(文字早已打完,loading 空转)。
	sess.FinishInput()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if sess.Endpoint() {
			final := strings.TrimSpace(sess.Finalize())
			if final != "" && granted {
				if strings.HasPrefix(final, typed) {
					if delta := final[len(typed):]; delta != "" {
						d.appendText(delta)
					}
				} else if typed == "" {
					d.appendText(final)
				} else if tail, ok := salvageTail(typed, final); ok {
					// 孤标点尾巴(重复句读)不打入——见 commitExact 同款注释
					if strings.Trim(tail, streamPuncts+" ") != "" {
						appLog.Printf("🔧 定稿尾部补救(云端回改致前缀失配):+%q", tail)
						d.appendText(tail)
					}
				} else {
					appLog.Printf("⚠️ 定稿与已提交不一致(无法对齐,保留已提交):已=%q 定=%q", typed, final)
				}
			}
			d.polishFlush(false)
			return
		}
		if p := sess.Partial(); granted && strings.HasPrefix(p, typed) && len(p) > len(typed) {
			d.appendText(p[len(typed):])
			typed = p
		}
		time.Sleep(150 * time.Millisecond)
	}
	appLog.Printf("⚠️ 会话收尾超时(8s),丢弃未返回的尾部")
	d.polishFlush(false)
}

// setStatus 显示状态行并更新文案(ptt 待机态整行隐藏,任何真实状态
// 都要先 Show 回来再改标题)。
func (d *dictation) setStatus(title string) {
	d.mStatus.Show()
	d.mStatus.SetTitle(title)
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
		d.setStatus("听写:开启中")
		d.mToggle.SetTitle("暂停听写")
	} else {
		d.setStatus("听写:已暂停")
		d.mToggle.SetTitle("开启听写")
	}
}

// audioLoop 消费音频并按模式分发:流式直喂会话;整句送 VAD,分段排队转写。
// 波形电平已在采音回调(feedLevel)就地投递,本循环只服务识别;每 2s 的
// 输入电平日志用于诊断采音问题。
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
			segCh <- segJob{d, eng, seg} // 转写(云端可达数秒)不阻塞音频线
		}
	}
}

// salvageTail 定稿尾部补救:云端定稿会回改早前文字(插/删标点、纠同音字),
// 前缀匹配失败时不再把整条定稿丢弃,而是拿已提交文本末 n 字(6/4/2 逐级退化)
// 作锚点在定稿中定位最后一次出现,其后即为本应续上的尾巴。
// 锚点候选额外包含"去掉末字符"版本:句尾标点常正是被云端改写的那个字
// (续说时句号→逗号),带标点锚点会全部失配(2026-09-25 实锤案例)。
// 返回 (尾巴, 是否已和解):所有锚点都找不到(定稿被整体重写)才放弃。
func salvageTail(committed, final string) (string, bool) {
	// 第一优先:字面锚点(全串;再去掉末字符——句尾标点常是被改写的那个)
	cr := []rune(committed)
	if tail, ok := tryAnchors(cr, final); ok {
		return tail, true
	}
	if len(cr) > 1 {
		if tail, ok := tryAnchors(cr[:len(cr)-1], final); ok {
			return tail, true
		}
	}
	// 第二优先:空格不敏感对齐——英文边界处云端会增删空格(partial " N" →
	// 定稿 "NRO"),字面锚点全失配时按去空格文本匹配,命中后映射回原文位置
	fStrip, fMap := stripSpaces(final)
	cStrip, _ := stripSpaces(committed)
	sr := []rune(cStrip)
	for _, src := range [][]rune{sr, trimLastRune(sr)} {
		for _, n := range []int{6, 4, 2} {
			if len(src) < n {
				continue
			}
			anchor := string(src[len(src)-n:])
			i := strings.LastIndex(fStrip, anchor)
			if i < 0 {
				continue
			}
			riEnd := utf8.RuneCountInString(fStrip[:i]) + n // 锚点在去空格文本中的结束 rune 位
			if riEnd >= len(fMap) {
				return "", true // 锚点到达定稿末尾,无新内容
			}
			fr := []rune(final)
			return string(fr[fMap[riEnd]:]), true
		}
	}
	return "", false
}

// tryAnchors 取 src 末 n 字(n=6/4/2 逐级退化)在 final 中定位最后一次出现,
// 返回其后内容;全部失配返回 false。
func tryAnchors(src []rune, final string) (string, bool) {
	for _, n := range []int{6, 4, 2} {
		if len(src) < n {
			continue
		}
		anchor := string(src[len(src)-n:])
		if i := strings.LastIndex(final, anchor); i >= 0 {
			return final[i+len(anchor):], true
		}
	}
	return "", false
}

// stripSpaces 去掉空格,返回去空格文本 + 各 rune 在原文中的 rune 下标映射。
func stripSpaces(s string) (string, []int) {
	var b strings.Builder
	m := []int{}
	for i, r := range []rune(s) {
		if r == ' ' {
			continue
		}
		b.WriteRune(r)
		m = append(m, i)
	}
	return b.String(), m
}

func trimLastRune(r []rune) []rune {
	if len(r) > 1 {
		return r[:len(r)-1]
	}
	return r
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
