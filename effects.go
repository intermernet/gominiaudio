package gominiaudio

// This file implements miniaudio's small effect processors: ma_panner,
// ma_fader, ma_gainer and ma_delay.

/**************************************************************************
Panner
**************************************************************************/

// PannerConfig mirrors ma_panner_config.
type PannerConfig struct {
	Format   Format
	Channels uint32
	Mode     PanMode
	Pan      float32
}

// PannerConfigInit mirrors ma_panner_config_init.
func PannerConfigInit(format Format, channels uint32) PannerConfig {
	return PannerConfig{Format: format, Channels: channels, Mode: PanModeBalance, Pan: 0}
}

// Panner mirrors ma_panner: pans stereo audio between the left and right
// channels. Non-stereo audio passes through unchanged.
type Panner struct {
	format   Format
	channels uint32
	mode     PanMode
	pan      float32 // -1..+1.
	scratch  s16Scratch
}

// NewPanner mirrors ma_panner_init.
func NewPanner(config PannerConfig) (*Panner, error) {
	if config.Channels == 0 {
		return nil, ErrInvalidArgs
	}
	return &Panner{format: config.Format, channels: config.Channels, mode: config.Mode, pan: config.Pan}, nil
}

// SetMode mirrors ma_panner_set_mode.
func (p *Panner) SetMode(mode PanMode) { p.mode = mode }

// Mode mirrors ma_panner_get_mode.
func (p *Panner) Mode() PanMode { return p.mode }

// SetPan mirrors ma_panner_set_pan. pan is between -1 (full left) and +1
// (full right).
func (p *Panner) SetPan(pan float32) {
	if pan < -1 {
		pan = -1
	}
	if pan > 1 {
		pan = 1
	}
	p.pan = pan
}

// Pan mirrors ma_panner_get_pan.
func (p *Panner) Pan() float32 { return p.pan }

// ProcessF32 applies panning to interleaved stereo f32 frames, in-place
// capable.
func (p *Panner) ProcessF32(dst, src []float32, frameCount uint64) error {
	n := int(frameCount)
	ch := int(p.channels)
	if len(src) < n*ch || len(dst) < n*ch {
		return ErrInvalidArgs
	}
	if p.channels != 2 || p.pan == 0 {
		if &dst[0] != &src[0] {
			copy(dst[:n*ch], src[:n*ch])
		}
		return nil
	}

	pan := p.pan
	switch p.mode {
	case PanModeBalance:
		// Reduce the opposite side's volume without blending.
		if pan > 0 {
			factor := 1 - pan
			for f := 0; f < n; f++ {
				dst[f*2+0] = src[f*2+0] * factor
				dst[f*2+1] = src[f*2+1]
			}
		} else {
			factor := 1 + pan
			for f := 0; f < n; f++ {
				dst[f*2+0] = src[f*2+0]
				dst[f*2+1] = src[f*2+1] * factor
			}
		}
	default: // PanModePan: blend one side into the other.
		if pan > 0 {
			for f := 0; f < n; f++ {
				l := src[f*2+0]
				r := src[f*2+1]
				moved := l * pan
				dst[f*2+0] = l - moved
				dst[f*2+1] = r + moved
			}
		} else {
			for f := 0; f < n; f++ {
				l := src[f*2+0]
				r := src[f*2+1]
				moved := r * -pan
				dst[f*2+0] = l + moved
				dst[f*2+1] = r - moved
			}
		}
	}
	return nil
}

// Process mirrors ma_panner_process_pcm_frames.
func (p *Panner) Process(dst, src []byte, frameCount uint64) error {
	switch p.format {
	case FormatF32:
		return p.ProcessF32(bytesToF32(dst), bytesToF32(src), frameCount)
	case FormatS16:
		return p.scratch.process(dst, src, frameCount, p.channels, p.ProcessF32)
	default:
		return ErrInvalidOperation
	}
}

/**************************************************************************
Fader
**************************************************************************/

// FaderConfig mirrors ma_fader_config.
type FaderConfig struct {
	Format     Format
	Channels   uint32
	SampleRate uint32
}

// FaderConfigInit mirrors ma_fader_config_init.
func FaderConfigInit(format Format, channels, sampleRate uint32) FaderConfig {
	return FaderConfig{Format: format, Channels: channels, SampleRate: sampleRate}
}

// Fader mirrors ma_fader: fades volume between two levels over time.
type Fader struct {
	config         FaderConfig
	volumeBeg      float32
	volumeEnd      float32
	lengthInFrames uint64
	cursorInFrames int64 // Can be negative for delayed fades.
	scratch        s16Scratch
}

// NewFader mirrors ma_fader_init.
func NewFader(config FaderConfig) (*Fader, error) {
	if config.Channels == 0 {
		return nil, ErrInvalidArgs
	}
	return &Fader{config: config, volumeBeg: 1, volumeEnd: 1}, nil
}

// SetFade mirrors ma_fader_set_fade.
func (f *Fader) SetFade(volumeBeg, volumeEnd float32, lengthInFrames uint64) {
	f.SetFadeEx(volumeBeg, volumeEnd, lengthInFrames, 0)
}

// SetFadeEx mirrors ma_fader_set_fade_ex with a start offset (negative to
// delay the fade).
func (f *Fader) SetFadeEx(volumeBeg, volumeEnd float32, lengthInFrames uint64, startOffsetInFrames int64) {
	if volumeBeg < 0 {
		volumeBeg = 0
	}
	f.volumeBeg = volumeBeg
	f.volumeEnd = volumeEnd
	f.lengthInFrames = lengthInFrames
	f.cursorInFrames = -startOffsetInFrames
}

// CurrentVolume mirrors ma_fader_get_current_volume.
func (f *Fader) CurrentVolume() float32 {
	if f.cursorInFrames < 0 {
		return f.volumeBeg
	}
	if uint64(f.cursorInFrames) >= f.lengthInFrames {
		return f.volumeEnd
	}
	a := float32(f.cursorInFrames) / float32(f.lengthInFrames)
	return f.volumeBeg + (f.volumeEnd-f.volumeBeg)*a
}

// ProcessF32 applies the fade to interleaved f32 frames, in-place capable.
func (f *Fader) ProcessF32(dst, src []float32, frameCount uint64) error {
	ch := int(f.config.Channels)
	n := int(frameCount)
	if len(src) < n*ch || len(dst) < n*ch {
		return ErrInvalidArgs
	}
	for i := 0; i < n; i++ {
		v := f.CurrentVolume()
		for c := 0; c < ch; c++ {
			dst[i*ch+c] = src[i*ch+c] * v
		}
		f.cursorInFrames++
	}
	return nil
}

// Process mirrors ma_fader_process_pcm_frames.
func (f *Fader) Process(dst, src []byte, frameCount uint64) error {
	switch f.config.Format {
	case FormatF32:
		return f.ProcessF32(bytesToF32(dst), bytesToF32(src), frameCount)
	case FormatS16:
		return f.scratch.process(dst, src, frameCount, f.config.Channels, f.ProcessF32)
	default:
		return ErrInvalidOperation
	}
}

// DataFormat mirrors ma_fader_get_data_format.
func (f *Fader) DataFormat() (Format, uint32, uint32) {
	return f.config.Format, f.config.Channels, f.config.SampleRate
}

/**************************************************************************
Gainer
**************************************************************************/

// GainerConfig mirrors ma_gainer_config.
type GainerConfig struct {
	Channels          uint32
	SmoothTimeInFrames uint32
}

// GainerConfigInit mirrors ma_gainer_config_init.
func GainerConfigInit(channels, smoothTimeInFrames uint32) GainerConfig {
	return GainerConfig{Channels: channels, SmoothTimeInFrames: smoothTimeInFrames}
}

// Gainer mirrors ma_gainer: applies per-channel gain with smoothing to avoid
// clicks. f32 only, like miniaudio.
type Gainer struct {
	config    GainerConfig
	t         uint32 // Frames since the last gain change, capped at SmoothTimeInFrames.
	oldGains  []float32
	newGains  []float32
}

// NewGainer mirrors ma_gainer_init.
func NewGainer(config GainerConfig) (*Gainer, error) {
	if config.Channels == 0 {
		return nil, ErrInvalidArgs
	}
	g := &Gainer{
		config:   config,
		t:        config.SmoothTimeInFrames, // Start converged.
		oldGains: make([]float32, config.Channels),
		newGains: make([]float32, config.Channels),
	}
	for i := range g.oldGains {
		g.oldGains[i] = 1
		g.newGains[i] = 1
	}
	return g, nil
}

// SetGain mirrors ma_gainer_set_gain: sets all channels to the same gain.
func (g *Gainer) SetGain(gain float32) error {
	for i := range g.newGains {
		g.oldGains[i] = g.currentGain(i)
		g.newGains[i] = gain
	}
	g.t = 0
	return nil
}

// SetGains mirrors ma_gainer_set_gains: per-channel gains.
func (g *Gainer) SetGains(gains []float32) error {
	if len(gains) < int(g.config.Channels) {
		return ErrInvalidArgs
	}
	for i := range g.newGains {
		g.oldGains[i] = g.currentGain(i)
		g.newGains[i] = gains[i]
	}
	g.t = 0
	return nil
}

// snapToTarget makes the current gains take effect immediately, skipping
// the smoothing ramp. Used for the first ever application of gains so a
// sound doesn't ramp in from a wrong initial level.
func (g *Gainer) snapToTarget() {
	copy(g.oldGains, g.newGains)
	g.t = g.config.SmoothTimeInFrames
}

func (g *Gainer) currentGain(ch int) float32 {
	if g.config.SmoothTimeInFrames == 0 || g.t >= g.config.SmoothTimeInFrames {
		return g.newGains[ch]
	}
	a := float32(g.t) / float32(g.config.SmoothTimeInFrames)
	return g.oldGains[ch] + (g.newGains[ch]-g.oldGains[ch])*a
}

// Process mirrors ma_gainer_process_pcm_frames on interleaved f32 frames,
// in-place capable.
func (g *Gainer) Process(dst, src []float32, frameCount uint64) error {
	ch := int(g.config.Channels)
	n := int(frameCount)
	if len(src) < n*ch || len(dst) < n*ch {
		return ErrInvalidArgs
	}
	// Ramping portion: interpolate gains incrementally. The per-sample
	// division and function call of currentGain are hoisted out; each
	// channel's gain advances by a constant step per frame.
	f := 0
	if g.t < g.config.SmoothTimeInFrames {
		inv := 1 / float32(g.config.SmoothTimeInFrames)
		switch ch {
		case 1:
			d0 := (g.newGains[0] - g.oldGains[0]) * inv
			gain := g.oldGains[0] + d0*float32(g.t)
			for ; f < n && g.t < g.config.SmoothTimeInFrames; f++ {
				dst[f] = src[f] * gain
				gain += d0
				g.t++
			}
		case 2:
			d0 := (g.newGains[0] - g.oldGains[0]) * inv
			d1 := (g.newGains[1] - g.oldGains[1]) * inv
			gain0 := g.oldGains[0] + d0*float32(g.t)
			gain1 := g.oldGains[1] + d1*float32(g.t)
			for ; f < n && g.t < g.config.SmoothTimeInFrames; f++ {
				dst[f*2] = src[f*2] * gain0
				dst[f*2+1] = src[f*2+1] * gain1
				gain0 += d0
				gain1 += d1
				g.t++
			}
		default:
			for ; f < n && g.t < g.config.SmoothTimeInFrames; f++ {
				a := float32(g.t) * inv
				for c := 0; c < ch; c++ {
					gain := g.oldGains[c] + (g.newGains[c]-g.oldGains[c])*a
					dst[f*ch+c] = src[f*ch+c] * gain
				}
				g.t++
			}
		}
	}

	// Converged portion: constant gains.
	if f < n {
		allOne := true
		for _, v := range g.newGains {
			if v != 1 {
				allOne = false
				break
			}
		}
		if allOne {
			if &dst[0] != &src[0] {
				copy(dst[f*ch:n*ch], src[f*ch:n*ch])
			}
			return nil
		}
		for ; f < n; f++ {
			for c := 0; c < ch; c++ {
				dst[f*ch+c] = src[f*ch+c] * g.newGains[c]
			}
		}
	}
	return nil
}

/**************************************************************************
Delay
**************************************************************************/

// DelayConfig mirrors ma_delay_config.
type DelayConfig struct {
	Channels      uint32
	SampleRate    uint32
	DelayInFrames uint32
	DelayStart    bool    // Delay the start of the output. When false, acts as an echo (dry included immediately).
	Wet           float32 // 0..1. Default 1.
	Dry           float32 // 0..1. Default 1.
	Decay         float32 // 0..1. Feedback decay. Default 0 (no feedback).
}

// DelayConfigInit mirrors ma_delay_config_init.
func DelayConfigInit(channels, sampleRate, delayInFrames uint32, decay float32) DelayConfig {
	return DelayConfig{
		Channels:      channels,
		SampleRate:    sampleRate,
		DelayInFrames: delayInFrames,
		DelayStart:    decay == 0,
		Wet:           1,
		Dry:           1,
		Decay:         decay,
	}
}

// Delay mirrors ma_delay: a delay/echo effect with feedback. f32 only.
type Delay struct {
	config       DelayConfig
	cursor       uint32
	bufferSizeInFrames uint32
	buffer       []float32
}

// NewDelay mirrors ma_delay_init.
func NewDelay(config DelayConfig) (*Delay, error) {
	if config.Channels == 0 || config.SampleRate == 0 || config.DelayInFrames == 0 {
		return nil, ErrInvalidArgs
	}
	if config.Decay < 0 || config.Decay > 1 {
		return nil, ErrInvalidArgs
	}
	return &Delay{
		config:             config,
		bufferSizeInFrames: config.DelayInFrames,
		buffer:             make([]float32, int(config.DelayInFrames)*int(config.Channels)),
	}, nil
}

// Process mirrors ma_delay_process_pcm_frames on interleaved f32 frames,
// in-place capable.
func (d *Delay) Process(dst, src []float32, frameCount uint64) error {
	ch := int(d.config.Channels)
	n := int(frameCount)
	if len(src) < n*ch || len(dst) < n*ch {
		return ErrInvalidArgs
	}
	wet, dry, decay := d.config.Wet, d.config.Dry, d.config.Decay

	for f := 0; f < n; f++ {
		base := int(d.cursor) * ch
		for c := 0; c < ch; c++ {
			x := src[f*ch+c]
			delayed := d.buffer[base+c]
			var y float32
			if d.config.DelayStart {
				// Delayed start: only the delayed signal is heard.
				y = delayed * wet
				d.buffer[base+c] = x*dry + delayed*decay
			} else {
				// Echo: dry passes immediately, echoes decay.
				y = x*dry + delayed*wet
				d.buffer[base+c] = x + delayed*decay
			}
			dst[f*ch+c] = y
		}
		d.cursor++
		if d.cursor >= d.bufferSizeInFrames {
			d.cursor = 0
		}
	}
	return nil
}

// SetWet mirrors ma_delay_set_wet.
func (d *Delay) SetWet(v float32) { d.config.Wet = v }

// Wet mirrors ma_delay_get_wet.
func (d *Delay) Wet() float32 { return d.config.Wet }

// SetDry mirrors ma_delay_set_dry.
func (d *Delay) SetDry(v float32) { d.config.Dry = v }

// Dry mirrors ma_delay_get_dry.
func (d *Delay) Dry() float32 { return d.config.Dry }

// SetDecay mirrors ma_delay_set_decay.
func (d *Delay) SetDecay(v float32) { d.config.Decay = v }

// Decay mirrors ma_delay_get_decay.
func (d *Delay) Decay() float32 { return d.config.Decay }
