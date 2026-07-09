// Command latency measures round-trip audio latency: it plays impulses on
// a playback device and detects them on a capture device, reporting the
// wall-clock delay between writing the impulse into the output stream and
// seeing it in the input stream.
//
// Route the playback device into the capture device with a loopback (a
// physical cable, a virtual cable such as VB-Audio Cable, or a monitor
// source) and run:
//
//	latency -out "CABLE In" -in "CABLE Output"
//
// The equivalent C program (miniaudio_latency.c) implements the same
// measurement with the original C miniaudio for a direct comparison.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	ma "github.com/intermernet/gominiaudio"
)

var (
	outName = flag.String("out", "", "substring of the playback device name (default: system default)")
	inName  = flag.String("in", "", "substring of the capture device name (default: system default)")
	rate      = flag.Uint("rate", 48000, "sample rate")
	period    = flag.Uint("period", 0, "period size in frames (0 = backend default)")
	trials    = flag.Int("trials", 10, "number of impulses to measure")
	exclusive = flag.Bool("exclusive", false, "use exclusive mode (WASAPI)")
	preseek   = flag.Uint("preseek", 0, "duplex pre-seek in frames (0 = default of 2 capture periods)")
)

const (
	impulseGapFrames = 24000 // Frames of silence between impulses (~500ms at 48k).
	threshold        = 0.25
)

type state struct {
	emitCountdown int64 // Frames until the next impulse.
	emitTimeNS    atomic.Int64
	armed         atomic.Bool
	results       chan time.Duration
}

func findDevice(ctx *ma.Context, deviceType ma.DeviceType, substr string) (*ma.DeviceID, string, error) {
	if substr == "" {
		return nil, "(default)", nil
	}
	devices, err := ctx.Devices(deviceType)
	if err != nil {
		return nil, "", err
	}
	for _, d := range devices {
		if strings.Contains(strings.ToLower(d.Name), strings.ToLower(substr)) {
			id := d.ID
			return &id, d.Name, nil
		}
	}
	return nil, "", fmt.Errorf("no device matching %q", substr)
}

func main() {
	flag.Parse()

	ctx, err := ma.InitContext(nil, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "context:", err)
		os.Exit(1)
	}
	defer ctx.Uninit()
	fmt.Println("backend:", ctx.Backend())

	outID, outLabel, err := findDevice(ctx, ma.DeviceTypePlayback, *outName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	inID, inLabel, err := findDevice(ctx, ma.DeviceTypeCapture, *inName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("playback: %s\ncapture:  %s\n", outLabel, inLabel)

	st := &state{
		emitCountdown: int64(*rate), // First impulse after 1s of warmup.
		results:       make(chan time.Duration, *trials),
	}

	cfg := ma.DeviceConfigInit(ma.DeviceTypeDuplex)
	cfg.SampleRate = uint32(*rate)
	cfg.PeriodSizeInFrames = uint32(*period)
	cfg.Playback.Format = ma.FormatF32
	cfg.Playback.Channels = 2
	cfg.Playback.DeviceID = outID
	cfg.Capture.Format = ma.FormatF32
	cfg.Capture.Channels = 2
	cfg.Capture.DeviceID = inID
	cfg.PerformanceProfile = ma.PerformanceProfileLowLatency
	cfg.DuplexPreSeekSizeInFrames = uint32(*preseek)
	if *exclusive {
		cfg.Playback.ShareMode = ma.ShareModeExclusive
		cfg.Capture.ShareMode = ma.ShareModeExclusive
	}

	measured := 0
	dev, err := ma.InitDevice(ctx, cfg, ma.DeviceCallbacks{
		Data: func(_ *ma.Device, output, input []byte, frameCount uint32) {
			out := f32s(output)
			in := f32s(input)

			// Detect the impulse in the captured input.
			if st.armed.Load() {
				for i := 0; i < len(in); i += 2 {
					v := in[i]
					if v > threshold || v < -threshold {
						if st.armed.Swap(false) {
							emit := st.emitTimeNS.Load()
							select {
							case st.results <- time.Duration(time.Now().UnixNano() - emit):
							default:
							}
						}
						break
					}
				}
			}

			// Generate output: silence, with a periodic impulse.
			for i := range out {
				out[i] = 0
			}
			st.emitCountdown -= int64(frameCount)
			if st.emitCountdown <= 0 && !st.armed.Load() {
				// Write a 16 frame full-scale burst at the start of this
				// buffer and arm the detector.
				n := 16
				if int(frameCount) < n {
					n = int(frameCount)
				}
				for i := 0; i < n; i++ {
					out[i*2] = 0.9
					out[i*2+1] = 0.9
				}
				st.emitTimeNS.Store(time.Now().UnixNano())
				st.armed.Store(true)
				st.emitCountdown = impulseGapFrames
			}
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "device:", err)
		os.Exit(1)
	}
	defer dev.Uninit()

	fmt.Printf("negotiated: playback period=%d frames, capture period=%d frames @ %dHz\n",
		dev.PlaybackInternalPeriodSizeInFrames(), dev.CaptureInternalPeriodSizeInFrames(), dev.SampleRate())

	if err := dev.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "start:", err)
		os.Exit(1)
	}

	var samples []time.Duration
	timeout := time.After(time.Duration(*trials)*time.Second + 10*time.Second)
	for measured < *trials {
		select {
		case d := <-st.results:
			measured++
			samples = append(samples, d)
			fmt.Printf("  impulse %2d: %7.2f ms\n", measured, float64(d.Microseconds())/1000)
		case <-timeout:
			fmt.Fprintln(os.Stderr, "timed out waiting for impulses; is the loopback routed?")
			measured = *trials
		}
	}
	dev.Stop()

	if len(samples) == 0 {
		os.Exit(1)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	var sum time.Duration
	for _, s := range samples {
		sum += s
	}
	fmt.Printf("\nround-trip latency over %d impulses:\n", len(samples))
	fmt.Printf("  min:    %7.2f ms\n", ms(samples[0]))
	fmt.Printf("  median: %7.2f ms\n", ms(samples[len(samples)/2]))
	fmt.Printf("  avg:    %7.2f ms\n", ms(sum/time.Duration(len(samples))))
	fmt.Printf("  max:    %7.2f ms\n", ms(samples[len(samples)-1]))
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// f32s reinterprets a byte slice as float32 samples.
func f32s(b []byte) []float32 {
	if len(b) < 4 {
		return nil
	}
	return unsafeF32(b)
}
