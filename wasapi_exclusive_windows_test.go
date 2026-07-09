//go:build windows

package gominiaudio

import (
	"math"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestWASAPIExclusiveCallbackFires verifies that an exclusive-mode playback
// device actually drives its data callback. This isolates the package from
// the caller: if the callback fires, the package is servicing the exclusive
// stream, so any "no output" is downstream (format conversion or the
// caller's own buffer handling). If it never fires, the bug is here.
//
// The test skips when exclusive mode is unavailable (no device, or the
// endpoint is already in use — exclusive mode requires sole ownership).
func TestWASAPIExclusiveCallbackFires(t *testing.T) {
	ctx, err := InitContext([]Backend{BackendWASAPI}, nil)
	if err != nil {
		t.Skip("WASAPI unavailable:", err)
	}
	defer ctx.Uninit()

	wf, err := NewWaveform(WaveformConfigInit(FormatF32, 2, 48000, WaveformTypeSine, 0.2, 440))
	if err != nil {
		t.Fatal(err)
	}

	var callbacks, framesTotal int64
	var clientNonZero atomic.Bool

	cfg := DeviceConfigInit(DeviceTypePlayback)
	cfg.Playback.Format = FormatF32
	cfg.Playback.Channels = 2
	cfg.SampleRate = 48000
	cfg.Playback.ShareMode = ShareModeExclusive

	dev, err := InitDevice(ctx, cfg, DeviceCallbacks{
		Data: func(_ *Device, out, _ []byte, frames uint32) {
			atomic.AddInt64(&callbacks, 1)
			atomic.AddInt64(&framesTotal, int64(frames))
			wf.Read(out, uint64(frames))
			for _, v := range bytesToF32(out) {
				if v != 0 {
					clientNonZero.Store(true)
					break
				}
			}
		},
	})
	if err != nil {
		t.Skip("exclusive-mode init failed (endpoint may be in use):", err)
	}
	defer dev.Uninit()

	f, c, r := dev.PlaybackInternalFormat()
	t.Logf("exclusive internal format: %v %dch %dHz, period=%d frames",
		f, c, r, dev.PlaybackInternalPeriodSizeInFrames())

	if err := dev.Start(); err != nil {
		t.Fatal("Start failed:", err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := dev.Stop(); err != nil {
		t.Error("Stop failed:", err)
	}

	cb := atomic.LoadInt64(&callbacks)
	total := atomic.LoadInt64(&framesTotal)
	t.Logf("callbacks=%d framesTotal=%d (~%.1f ms of audio)", cb, total, float64(total)/48.0)

	if cb == 0 {
		t.Fatal("exclusive-mode data callback never fired: the package is NOT driving the device")
	}
	if !clientNonZero.Load() {
		t.Fatal("callback fired but the client buffer was silent")
	}
	// 300ms at 48kHz is ~14400 frames; allow generous slack for startup.
	if total < 4800 {
		t.Errorf("only %d frames requested in 300ms; the exclusive stream is starved", total)
	}
}

// TestWASAPIExclusiveMinimalConfig exercises the path a caller hits when
// they only flip ShareMode to exclusive and leave format/channels/rate at
// their zero (auto) defaults. A common cause of "no output" is that this
// combination fails to negotiate and the caller ignores the init error, so
// this test asserts it either works or returns a clear error (never a
// running-but-silent device).
func TestWASAPIExclusiveMinimalConfig(t *testing.T) {
	ctx, err := InitContext([]Backend{BackendWASAPI}, nil)
	if err != nil {
		t.Skip("WASAPI unavailable:", err)
	}
	defer ctx.Uninit()

	cfg := DeviceConfigInit(DeviceTypePlayback)
	cfg.Playback.ShareMode = ShareModeExclusive
	// Everything else left at 0 -> auto-negotiate from the device's native
	// (physical) format.

	var callbacks int64
	dev, err := InitDevice(ctx, cfg, DeviceCallbacks{
		Data: func(d *Device, out, _ []byte, frames uint32) {
			atomic.AddInt64(&callbacks, 1)
			// The client format is whatever was negotiated; fill with a
			// low-amplitude ramp in that format so output is non-silent
			// regardless of the format.
			fillNonSilent(d, out, frames)
		},
	})
	if err != nil {
		t.Skipf("minimal exclusive config did not negotiate on this device (%v); "+
			"callers must check this error rather than assume success", err)
	}
	defer dev.Uninit()

	f, c, r := dev.PlaybackInternalFormat()
	cf, cc := dev.PlaybackFormat()
	t.Logf("negotiated internal=%v %dch %dHz, client=%v %dch %dHz",
		f, c, r, cf, cc, dev.SampleRate())

	if err := dev.Start(); err != nil {
		t.Fatal("Start failed:", err)
	}
	time.Sleep(200 * time.Millisecond)
	dev.Stop()

	if atomic.LoadInt64(&callbacks) == 0 {
		t.Fatal("minimal-config exclusive device started but never called back")
	}
}

// fillNonSilent writes a small ramp into out, in the device's client
// playback format, so the buffer is audibly non-zero.
func fillNonSilent(d *Device, out []byte, frames uint32) {
	format, channels := d.PlaybackFormat()
	samples := int(frames) * int(channels)
	tmp := make([]float32, samples)
	for i := range tmp {
		tmp[i] = float32((i%64))/64*0.2 - 0.1
	}
	_ = PCMConvert(out, format, f32ToBytes(tmp), FormatF32, uint64(samples), DitherModeNone)
}

// TestWASAPIExclusiveLoopback plays a tone in exclusive mode and captures it
// back through a hardware/virtual loopback, proving that audio actually
// leaves the device (format conversion included). It is opt-in because it
// needs a routed loopback:
//
//	set GOMINIAUDIO_LOOPBACK=1
//	set GOMINIAUDIO_OUT=CABLE In
//	set GOMINIAUDIO_IN=CABLE Output
//	go test -run TestWASAPIExclusiveLoopback -v
//
// The playback device is opened exclusively; the capture device is opened
// shared (exclusive loopback capture is not always available).
func TestWASAPIExclusiveLoopback(t *testing.T) {
	if os.Getenv("GOMINIAUDIO_LOOPBACK") == "" {
		t.Skip("set GOMINIAUDIO_LOOPBACK=1 with GOMINIAUDIO_OUT/GOMINIAUDIO_IN to run")
	}
	outName := os.Getenv("GOMINIAUDIO_OUT")
	inName := os.Getenv("GOMINIAUDIO_IN")

	ctx, err := InitContext([]Backend{BackendWASAPI}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ctx.Uninit()

	find := func(dt DeviceType, sub string) *DeviceID {
		if sub == "" {
			return nil
		}
		devs, err := ctx.Devices(dt)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range devs {
			if strings.Contains(strings.ToLower(d.Name), strings.ToLower(sub)) {
				id := d.ID
				return &id
			}
		}
		t.Fatalf("no device matching %q", sub)
		return nil
	}
	outID := find(DeviceTypePlayback, outName)
	inID := find(DeviceTypeCapture, inName)

	// Capture side (shared): accumulate RMS of what arrives.
	var mu sync.Mutex
	var sumSq float64
	var sampleCount int64

	capCfg := DeviceConfigInit(DeviceTypeCapture)
	capCfg.Capture.DeviceID = inID
	capCfg.Capture.Format = FormatF32
	capCfg.Capture.Channels = 2
	capCfg.SampleRate = 48000
	capDev, err := InitDevice(ctx, capCfg, DeviceCallbacks{
		Data: func(_ *Device, _, in []byte, frames uint32) {
			mu.Lock()
			for _, v := range bytesToF32(in) {
				sumSq += float64(v) * float64(v)
				sampleCount++
			}
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal("capture init:", err)
	}
	defer capDev.Uninit()

	// Playback side (exclusive): full-scale sine so it is unmistakable.
	wf, _ := NewWaveform(WaveformConfigInit(FormatF32, 2, 48000, WaveformTypeSine, 0.5, 440))
	playCfg := DeviceConfigInit(DeviceTypePlayback)
	playCfg.Playback.DeviceID = outID
	playCfg.Playback.Format = FormatF32
	playCfg.Playback.Channels = 2
	playCfg.SampleRate = 48000
	playCfg.Playback.ShareMode = ShareModeExclusive
	playDev, err := InitDevice(ctx, playCfg, DeviceCallbacks{
		Data: func(_ *Device, out, _ []byte, frames uint32) {
			wf.Read(out, uint64(frames))
		},
	})
	if err != nil {
		t.Fatal("exclusive playback init:", err)
	}
	defer playDev.Uninit()

	pf, pc, pr := playDev.PlaybackInternalFormat()
	t.Logf("exclusive playback: %v %dch %dHz period=%d", pf, pc, pr, playDev.PlaybackInternalPeriodSizeInFrames())

	if err := capDev.Start(); err != nil {
		t.Fatal(err)
	}
	if err := playDev.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	playDev.Stop()
	capDev.Stop()

	mu.Lock()
	n := sampleCount
	ss := sumSq
	mu.Unlock()
	if n == 0 {
		t.Fatal("no capture data at all — is the loopback routed?")
	}
	rms := math.Sqrt(ss / float64(n))
	t.Logf("captured %d samples, RMS=%.4f", n, rms)

	// A 0.5-amplitude sine has RMS ~0.354. Anything above the noise floor
	// proves exclusive-mode audio reached the wire.
	if rms < 0.01 {
		t.Fatalf("exclusive-mode output was silent at the capture side (RMS=%.5f)", rms)
	}
}
