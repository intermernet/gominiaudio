package gominiaudio

import (
	"sync"
	"time"
)

// The null backend mirrors miniaudio's null backend: a fake device driven by
// a timer that consumes/produces audio without touching any hardware. It is
// available on all platforms and is useful for testing.

type nullContext struct{}

func (n *nullContext) init(*ContextConfig) error { return nil }
func (n *nullContext) uninit() error             { return nil }
func (n *nullContext) backendID() Backend        { return BackendNull }

func (n *nullContext) enumerateDevices() ([]DeviceInfo, []DeviceInfo, error) {
	playback := []DeviceInfo{{
		ID:        deviceIDFromString("null-playback"),
		Name:      "NULL Playback Device",
		IsDefault: true,
		Formats:   []DeviceNativeDataFormat{{Format: FormatUnknown, Channels: 0, SampleRate: 0}},
	}}
	capture := []DeviceInfo{{
		ID:        deviceIDFromString("null-capture"),
		Name:      "NULL Capture Device",
		IsDefault: true,
		Formats:   []DeviceNativeDataFormat{{Format: FormatUnknown, Channels: 0, SampleRate: 0}},
	}}
	return playback, capture, nil
}

func (n *nullContext) deviceInfo(deviceType DeviceType, id *DeviceID) (DeviceInfo, error) {
	playback, capture, _ := n.enumerateDevices()
	if deviceType == DeviceTypePlayback {
		return playback[0], nil
	}
	return capture[0], nil
}

func (n *nullContext) newDevice(d *Device, config *DeviceConfig) (deviceBackend, error) {
	dev := &nullDevice{device: d}

	format := config.Playback.Format
	channels := config.Playback.Channels
	if d.deviceType == DeviceTypeCapture || d.deviceType == DeviceTypeLoopback {
		format = config.Capture.Format
		channels = config.Capture.Channels
	}
	if format == FormatUnknown {
		format = DefaultFormat
	}
	if channels == 0 {
		channels = DefaultChannels
	}
	sampleRate := config.SampleRate
	if sampleRate == 0 {
		sampleRate = DefaultSampleRate
	}
	period := d.calculatePeriodSizeInFrames(sampleRate)

	if d.deviceType&DeviceTypePlayback != 0 {
		d.setInternalFormat(DeviceTypePlayback, format, channels, sampleRate, nil, period, config.Periods, "NULL Playback Device")
	}
	if d.deviceType&DeviceTypeCapture != 0 || d.deviceType == DeviceTypeLoopback {
		d.setInternalFormat(DeviceTypeCapture, format, channels, sampleRate, nil, period, config.Periods, "NULL Capture Device")
	}

	dev.format = format
	dev.channels = channels
	dev.sampleRate = sampleRate
	dev.period = period
	return dev, nil
}

type nullDevice struct {
	device     *Device
	format     Format
	channels   uint32
	sampleRate uint32
	period     uint32

	mu     sync.Mutex
	stopCh chan struct{}
	doneCh chan struct{}
	buf    []byte
}

func (nd *nullDevice) start() error {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	if nd.stopCh != nil {
		return ErrDeviceNotStopped
	}
	nd.stopCh = make(chan struct{})
	nd.doneCh = make(chan struct{})
	bpf := FrameSizeInBytes(nd.format, nd.channels)
	if nd.buf == nil {
		nd.buf = make([]byte, int(nd.period)*bpf)
	}
	go nd.run(nd.stopCh, nd.doneCh)
	return nil
}

func (nd *nullDevice) run(stop, done chan struct{}) {
	defer close(done)
	interval := time.Duration(uint64(nd.period) * uint64(time.Second) / uint64(nd.sampleRate))
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			d := nd.device
			if d.deviceType&DeviceTypeCapture != 0 || d.deviceType == DeviceTypeLoopback {
				// Produce silence as captured data.
				SilencePCMFrames(nd.buf, uint64(nd.period), nd.format, nd.channels)
				d.handleCapture(nd.buf, nd.period)
			}
			if d.deviceType&DeviceTypePlayback != 0 {
				// Consume playback data into the void.
				d.handlePlayback(nd.buf, nd.period)
			}
		}
	}
}

func (nd *nullDevice) stopDevice() error {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	if nd.stopCh == nil {
		return nil
	}
	close(nd.stopCh)
	<-nd.doneCh
	nd.stopCh = nil
	nd.doneCh = nil
	return nil
}

func (nd *nullDevice) stop() error   { return nd.stopDevice() }
func (nd *nullDevice) uninit() error { return nd.stopDevice() }
