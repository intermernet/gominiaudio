// Command passthrough streams the default capture device to the default
// playback device (a duplex device), demonstrating the low-latency device
// API. Press Enter to stop.
package main

import (
	"bufio"
	"fmt"
	"os"

	ma "github.com/intermernet/gominiaudio"
)

func main() {
	ctx, err := ma.InitContext(nil, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to initialize context:", err)
		os.Exit(1)
	}
	defer ctx.Uninit()
	fmt.Println("backend:", ctx.Backend())

	cfg := ma.DeviceConfigInit(ma.DeviceTypeDuplex)
	cfg.SampleRate = 48000
	cfg.Playback.Format = ma.FormatF32
	cfg.Playback.Channels = 2
	cfg.Capture.Format = ma.FormatF32
	cfg.Capture.Channels = 2
	cfg.PerformanceProfile = ma.PerformanceProfileLowLatency

	dev, err := ma.InitDevice(ctx, cfg, ma.DeviceCallbacks{
		Data: func(_ *ma.Device, output, input []byte, frameCount uint32) {
			// Echo the captured input straight to the output. Both sides
			// use the same client format, so this is a plain copy.
			copy(output, input)
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to initialize duplex device:", err)
		os.Exit(1)
	}
	defer dev.Uninit()

	pf, pc, pr := dev.PlaybackInternalFormat()
	cf, cc, cr := dev.CaptureInternalFormat()
	fmt.Printf("capture:  %q %v %dch %dHz period=%d frames\n",
		dev.CaptureName(), cf, cc, cr, dev.CaptureInternalPeriodSizeInFrames())
	fmt.Printf("playback: %q %v %dch %dHz period=%d frames\n",
		dev.PlaybackName(), pf, pc, pr, dev.PlaybackInternalPeriodSizeInFrames())

	if err := dev.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "failed to start device:", err)
		os.Exit(1)
	}
	fmt.Println("streaming input to output... press Enter to stop")
	bufio.NewReader(os.Stdin).ReadString('\n')

	if err := dev.Stop(); err != nil {
		fmt.Fprintln(os.Stderr, "failed to stop device:", err)
		os.Exit(1)
	}
	fmt.Println("stopped")
}
