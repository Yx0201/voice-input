// Package capture 负责麦克风采集(malgo/miniaudio,CoreAudio 后端)。
// 输出 16kHz 单声道 float32 PCM(±1.0),回调在音频线程上,
// 因此只做入队,不做任何重活。
package capture

import (
	"fmt"
	"strings"
	"unsafe"

	"github.com/gen2brain/malgo"
)

// ListCaptureDeviceNames 枚举当前可用的输入设备名(用于日志与配置提示)。
func ListCaptureDeviceNames() ([]string, error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("初始化音频上下文失败: %w", err)
	}
	defer func() {
		ctx.Uninit()
		ctx.Free()
	}()
	infos, err := ctx.Devices(malgo.Capture)
	if err != nil {
		return nil, fmt.Errorf("枚举输入设备失败: %w", err)
	}
	names := make([]string, 0, len(infos))
	for i := range infos {
		if n := infos[i].Name(); n != "" {
			names = append(names, n)
		}
	}
	return names, nil
}

// Mic 封装一个持续采集的麦克风设备。
type Mic struct {
	ctx    *malgo.AllocatedContext
	device *malgo.Device
}

// NewMic 打开麦克风并开始采集。deviceName 为空用系统默认输入;
// 否则对设备名做子串匹配(如 "AirPods"、"MacBook")。
// onPCM 在音频线程回调,必须轻量(建议只写 channel)。
func NewMic(sampleRate, channels uint32, deviceName string, onPCM func(pcm []float32)) (*Mic, error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("初始化音频上下文失败(麦克风权限?): %w", err)
	}

	cfg := malgo.DefaultDeviceConfig(malgo.Capture)
	cfg.Capture.Format = malgo.FormatS16
	cfg.Capture.Channels = channels
	cfg.SampleRate = sampleRate
	cfg.PeriodSizeInMilliseconds = 20

	if deviceName != "" {
		infos, err := ctx.Devices(malgo.Capture)
		if err != nil {
			ctx.Uninit()
			ctx.Free()
			return nil, fmt.Errorf("枚举输入设备失败: %w", err)
		}
		var matched *malgo.DeviceID
		for i := range infos {
			if strings.Contains(strings.ToLower(infos[i].Name()), strings.ToLower(deviceName)) {
				id := infos[i].ID
				matched = &id
				break
			}
		}
		if matched == nil {
			available := make([]string, 0, len(infos))
			for i := range infos {
				available = append(available, infos[i].Name())
			}
			ctx.Uninit()
			ctx.Free()
			return nil, fmt.Errorf("找不到匹配 %q 的输入设备;可用: %s",
				deviceName, strings.Join(available, " | "))
		}
		cfg.Capture.DeviceID = unsafe.Pointer(matched)
	}

	onRecv := func(_, input []byte, frameCount uint32) {
		n := len(input) / 2
		if n == 0 {
			return
		}
		pcm := make([]float32, n)
		for i := 0; i < n; i++ {
			v := int16(uint16(input[2*i]) | uint16(input[2*i+1])<<8)
			pcm[i] = float32(v) / 32768.0
		}
		onPCM(pcm)
	}

	device, err := malgo.InitDevice(ctx.Context, cfg, malgo.DeviceCallbacks{Data: onRecv})
	if err != nil {
		ctx.Uninit()
		ctx.Free()
		return nil, fmt.Errorf("打开麦克风失败(检查 系统设置→隐私与安全性→麦克风): %w", err)
	}

	if err := device.Start(); err != nil {
		device.Uninit()
		ctx.Uninit()
		ctx.Free()
		return nil, fmt.Errorf("启动麦克风失败: %w", err)
	}
	return &Mic{ctx: ctx, device: device}, nil
}

// Close 停止采集并释放资源。
func (m *Mic) Close() {
	m.device.Uninit()
	m.ctx.Uninit()
	m.ctx.Free()
}

// Start 恢复采集(释放/恢复 麦克风占用指示)。
func (m *Mic) Start() error { return m.device.Start() }

// Stop 暂停采集(系统状态栏的麦克风橙点会消失)。
func (m *Mic) Stop() error { return m.device.Stop() }
