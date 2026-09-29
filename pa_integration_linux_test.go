//go:build linux && !android

package gominiaudio

import (
	"math"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// Integration tests against a real PulseAudio daemon. They skip when no
// server is reachable, so they are harmless on machines without one (and
// on PipeWire boxes they exercise its pulse-server compatibility layer).
//
// TestPulsePlaybackTone actually makes noise, so it additionally requires
// GOMINIAUDIO_AUDIO_TEST=1.

// requirePulseServer skips the test unless the daemon socket accepts a
// connection.
func requirePulseServer(t *testing.T) {
	t.Helper()
	path := paSocketPath()
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		t.Skipf("no PulseAudio server at %s: %v", path, err)
	}
	conn.Close()
}

// TestPulseConnectAndEnumerate covers the handshake and both info lists:
// connecting proves AUTH, SET_CLIENT_NAME and the negotiated version are
// right, and enumeration proves the sink/source tagstruct parsing matches
// what the server actually sends.
func TestPulseConnectAndEnumerate(t *testing.T) {
	requirePulseServer(t)

	ctx, err := InitContext([]Backend{BackendPulseAudio}, nil)
	if err != nil {
		t.Fatalf("InitContext: %v", err)
	}
	defer ctx.Uninit()

	if got := ctx.Backend(); got != BackendPulseAudio {
		t.Fatalf("backend = %v, want PulseAudio", got)
	}

	playback, err := ctx.Devices(DeviceTypePlayback)
	if err != nil {
		t.Fatalf("Devices(playback): %v", err)
	}
	if len(playback) == 0 {
		t.Fatal("no playback devices reported")
	}
	for _, d := range playback {
		t.Logf("sink: id=%q name=%q default=%v formats=%+v",
			d.ID.String(), d.Name, d.IsDefault, d.Formats)
		if d.Name == "" {
			t.Error("sink reported with an empty name")
		}
	}

	capture, err := ctx.Devices(DeviceTypeCapture)
	if err != nil {
		t.Fatalf("Devices(capture): %v", err)
	}
	for _, d := range capture {
		t.Logf("source: id=%q name=%q default=%v", d.ID.String(), d.Name, d.IsDefault)
	}

	// The default device lookup goes through a separate code path.
	info, err := ctx.backend.deviceInfo(DeviceTypePlayback, nil)
	if err != nil {
		t.Fatalf("deviceInfo(default): %v", err)
	}
	t.Logf("default sink: %q", info.Name)
}

// TestPulseOpenDeviceNoAudio opens and starts a playback device, feeding
// silence. It exercises CREATE_PLAYBACK_STREAM, the REQUEST/data loop and
// teardown without being audible.
func TestPulseOpenDeviceNoAudio(t *testing.T) {
	requirePulseServer(t)

	ctx, err := InitContext([]Backend{BackendPulseAudio}, nil)
	if err != nil {
		t.Fatalf("InitContext: %v", err)
	}
	defer ctx.Uninit()

	var frames atomic.Uint64
	cfg := DeviceConfigInit(DeviceTypePlayback)
	cfg.SampleRate = 48000
	cfg.Playback.Format = FormatF32
	cfg.Playback.Channels = 2

	dev, err := InitDevice(ctx, cfg, DeviceCallbacks{
		Data: func(d *Device, output, input []byte, frameCount uint32) {
			frames.Add(uint64(frameCount))
			// Leave output silent.
		},
	})
	if err != nil {
		t.Fatalf("InitDevice: %v", err)
	}
	defer dev.Uninit()

	format, channels, rate := dev.PlaybackInternalFormat()
	t.Logf("negotiated: format=%v channels=%d rate=%d period=%d",
		format, channels, rate, dev.PlaybackInternalPeriodSizeInFrames())

	if err := dev.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(600 * time.Millisecond)
	if err := dev.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	got := frames.Load()
	t.Logf("callback delivered %d frames", got)
	if got == 0 {
		t.Error("data callback was never invoked: the server never requested audio")
	}
	// Half a second at 48k is 24000 frames; allow for startup and
	// scheduling by requiring a quarter of that.
	if got < 6000 {
		t.Errorf("only %d frames in 600ms, expected the stream to run continuously", got)
	}
}

// TestPulseCaptureNoAudio opens a capture device and checks data arrives.
func TestPulseCaptureNoAudio(t *testing.T) {
	requirePulseServer(t)

	ctx, err := InitContext([]Backend{BackendPulseAudio}, nil)
	if err != nil {
		t.Fatalf("InitContext: %v", err)
	}
	defer ctx.Uninit()

	sources, err := ctx.Devices(DeviceTypeCapture)
	if err != nil || len(sources) == 0 {
		t.Skip("no capture sources available")
	}

	var frames atomic.Uint64
	cfg := DeviceConfigInit(DeviceTypeCapture)
	cfg.SampleRate = 48000
	cfg.Capture.Format = FormatF32
	cfg.Capture.Channels = 2

	dev, err := InitDevice(ctx, cfg, DeviceCallbacks{
		Data: func(d *Device, output, input []byte, frameCount uint32) {
			frames.Add(uint64(frameCount))
		},
	})
	if err != nil {
		t.Fatalf("InitDevice: %v", err)
	}
	defer dev.Uninit()

	if err := dev.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(600 * time.Millisecond)
	if err := dev.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	got := frames.Load()
	t.Logf("capture delivered %d frames", got)
	if got == 0 {
		t.Error("capture callback was never invoked")
	}
}

// TestPulsePlaybackTone plays an audible sine so the path can be confirmed
// by ear. It is opt-in because it makes noise.
func TestPulsePlaybackTone(t *testing.T) {
	if os.Getenv("GOMINIAUDIO_AUDIO_TEST") != "1" {
		t.Skip("set GOMINIAUDIO_AUDIO_TEST=1 to play an audible tone")
	}
	requirePulseServer(t)

	ctx, err := InitContext([]Backend{BackendPulseAudio}, nil)
	if err != nil {
		t.Fatalf("InitContext: %v", err)
	}
	defer ctx.Uninit()

	const freq = 440.0
	var phase float64

	cfg := DeviceConfigInit(DeviceTypePlayback)
	cfg.SampleRate = 48000
	cfg.Playback.Format = FormatF32
	cfg.Playback.Channels = 2

	dev, err := InitDevice(ctx, cfg, DeviceCallbacks{
		Data: func(d *Device, output, input []byte, frameCount uint32) {
			step := 2 * math.Pi * freq / 48000
			out := bytesToF32(output)
			for i := uint32(0); i < frameCount; i++ {
				v := float32(math.Sin(phase) * 0.2)
				out[i*2] = v
				out[i*2+1] = v
				phase += step
				if phase > 2*math.Pi {
					phase -= 2 * math.Pi
				}
			}
		},
	})
	if err != nil {
		t.Fatalf("InitDevice: %v", err)
	}
	defer dev.Uninit()

	if err := dev.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Log("playing a 440 Hz tone for 2 seconds")
	time.Sleep(2 * time.Second)
	if err := dev.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
