package gominiaudio

import "math"

// This file implements miniaudio's filter suite: biquad, first and second
// order low/high-pass filters, Nth order low/high/band-pass cascades, notch,
// peaking EQ and shelf filters. Coefficients are computed in float64 and
// samples are processed as float32, matching miniaudio's f32 path.

const (
	// MaxFilterOrder mirrors MA_MAX_FILTER_ORDER.
	MaxFilterOrder = 8

	// DefaultFilterQ mirrors MA_DEFAULT_RESAMPLER_LPF... it's sqrt(0.5),
	// the Butterworth Q for a single second order section.
	DefaultFilterQ = 0.707106781185474984
)

// BiquadConfig mirrors ma_biquad_config. Coefficients are specified
// unnormalized; they are divided by A0 at init time.
type BiquadConfig struct {
	Format     Format // FormatF32 or FormatS16.
	Channels   uint32
	B0, B1, B2 float64
	A0, A1, A2 float64
}

// BiquadConfigInit mirrors ma_biquad_config_init.
func BiquadConfigInit(format Format, channels uint32, b0, b1, b2, a0, a1, a2 float64) BiquadConfig {
	return BiquadConfig{Format: format, Channels: channels, B0: b0, B1: b1, B2: b2, A0: a0, A1: a1, A2: a2}
}

// Biquad mirrors ma_biquad: a second order filter implemented in transposed
// direct form 2.
type Biquad struct {
	format             Format
	channels           uint32
	b0, b1, b2, a1, a2 float32
	r1, r2             []float32 // Per-channel state.
	scratch            s16Scratch
}

// NewBiquad mirrors ma_biquad_init.
func NewBiquad(config BiquadConfig) (*Biquad, error) {
	if config.Channels == 0 {
		return nil, ErrInvalidArgs
	}
	if config.Format != FormatF32 && config.Format != FormatS16 {
		return nil, ErrInvalidArgs
	}
	bq := &Biquad{
		format:   config.Format,
		channels: config.Channels,
		r1:       make([]float32, config.Channels),
		r2:       make([]float32, config.Channels),
	}
	if err := bq.Reinit(config); err != nil {
		return nil, err
	}
	return bq, nil
}

// Reinit mirrors ma_biquad_reinit. Format and channel count must not change.
func (bq *Biquad) Reinit(config BiquadConfig) error {
	if config.A0 == 0 {
		return ErrInvalidArgs
	}
	if config.Channels != bq.channels || config.Format != bq.format {
		return ErrInvalidOperation
	}
	bq.b0 = float32(config.B0 / config.A0)
	bq.b1 = float32(config.B1 / config.A0)
	bq.b2 = float32(config.B2 / config.A0)
	bq.a1 = float32(config.A1 / config.A0)
	bq.a2 = float32(config.A2 / config.A0)
	return nil
}

// Clear resets the filter state, mirroring ma_biquad_clear_cache.
func (bq *Biquad) Clear() {
	for i := range bq.r1 {
		bq.r1[i] = 0
		bq.r2[i] = 0
	}
}

// LatencyInFrames mirrors ma_biquad_get_latency: 2 frames.
func (bq *Biquad) LatencyInFrames() uint32 { return 2 }

// ProcessF32 processes interleaved f32 frames. dst and src may be the same
// slice for in-place processing. All arithmetic is done in float32; the
// coefficients and state were normalised to float32 at Reinit time.
func (bq *Biquad) ProcessF32(dst, src []float32, frameCount uint64) error {
	ch := int(bq.channels)
	n := int(frameCount)
	if len(src) < n*ch || len(dst) < n*ch {
		return ErrInvalidArgs
	}
	biquadProcessF32Impl(dst, src, bq.r1[:ch], bq.r2[:ch],
		bq.b0, bq.b1, bq.b2, bq.a1, bq.a2, n, ch)
	return nil
}

// Process mirrors ma_biquad_process_pcm_frames for the configured format.
func (bq *Biquad) Process(dst, src []byte, frameCount uint64) error {
	switch bq.format {
	case FormatF32:
		return bq.ProcessF32(bytesToF32(dst), bytesToF32(src), frameCount)
	case FormatS16:
		return bq.scratch.process(dst, src, frameCount, bq.channels, bq.ProcessF32)
	default:
		return ErrInvalidOperation
	}
}

// s16Scratch adapts an f32 processor to s16 buffers. The scratch buffer is
// reused across calls so the audio path stays allocation-free once warmed up.
type s16Scratch struct {
	buf []float32
}

func (s *s16Scratch) process(dst, src []byte, frameCount uint64, channels uint32, proc func(dst, src []float32, frameCount uint64) error) error {
	sampleCount := int(frameCount) * int(channels)
	if cap(s.buf) < sampleCount {
		s.buf = make([]float32, sampleCount)
	}
	tmp := s.buf[:sampleCount]
	if err := PCMConvert(f32ToBytes(tmp), FormatF32, src, FormatS16, uint64(sampleCount), DitherModeNone); err != nil {
		return err
	}
	if err := proc(tmp, tmp, frameCount); err != nil {
		return err
	}
	return PCMConvert(dst, FormatS16, f32ToBytes(tmp), FormatF32, uint64(sampleCount), DitherModeNone)
}

// biquadProcessF32Impl is selected at init time; defaults to scalar.
var biquadProcessF32Impl = biquadProcessF32Scalar

// biquadProcessF32Scalar is the portable Biquad inner loop. Mono and stereo
// (the overwhelmingly common cases) keep the filter state in locals so the
// frame loop runs register-resident with no per-sample state slice traffic.
func biquadProcessF32Scalar(dst, src, r1, r2 []float32, b0, b1, b2, a1, a2 float32, n, ch int) {
	switch ch {
	case 1:
		s1, s2 := r1[0], r2[0]
		for f := 0; f < n; f++ {
			x := src[f]
			y := b0*x + s1
			s1 = b1*x - a1*y + s2
			s2 = b2*x - a2*y
			dst[f] = y
		}
		r1[0], r2[0] = s1, s2
	case 2:
		s1l, s2l := r1[0], r2[0]
		s1r, s2r := r1[1], r2[1]
		for f := 0; f < n; f++ {
			xl := src[f*2]
			xr := src[f*2+1]
			yl := b0*xl + s1l
			yr := b0*xr + s1r
			s1l = b1*xl - a1*yl + s2l
			s1r = b1*xr - a1*yr + s2r
			s2l = b2*xl - a2*yl
			s2r = b2*xr - a2*yr
			dst[f*2] = yl
			dst[f*2+1] = yr
		}
		r1[0], r2[0] = s1l, s2l
		r1[1], r2[1] = s1r, s2r
	default:
		for f := 0; f < n; f++ {
			for c := 0; c < ch; c++ {
				x := src[f*ch+c]
				y := b0*x + r1[c]
				r1[c] = b1*x - a1*y + r2[c]
				r2[c] = b2*x - a2*y
				dst[f*ch+c] = y
			}
		}
	}
}

/**************************************************************************
First order low-pass filter (LPF1)
**************************************************************************/

// LPF1Config / LPF2Config mirror ma_lpf1_config / ma_lpf2_config.
type LPF1Config struct {
	Format          Format
	Channels        uint32
	SampleRate      uint32
	CutoffFrequency float64
	Q               float64 // Only used by second order filters.
}

// LPF2Config is the same shape as LPF1Config, mirroring miniaudio.
type LPF2Config = LPF1Config

// LPF1ConfigInit mirrors ma_lpf1_config_init.
func LPF1ConfigInit(format Format, channels, sampleRate uint32, cutoffFrequency float64) LPF1Config {
	return LPF1Config{Format: format, Channels: channels, SampleRate: sampleRate, CutoffFrequency: cutoffFrequency}
}

// LPF2ConfigInit mirrors ma_lpf2_config_init.
func LPF2ConfigInit(format Format, channels, sampleRate uint32, cutoffFrequency, q float64) LPF2Config {
	return LPF2Config{Format: format, Channels: channels, SampleRate: sampleRate, CutoffFrequency: cutoffFrequency, Q: q}
}

// LPF1 mirrors ma_lpf1.
type LPF1 struct {
	format   Format
	channels uint32
	a        float64
	r1       []float64
	scratch  s16Scratch
}

// NewLPF1 mirrors ma_lpf1_init.
func NewLPF1(config LPF1Config) (*LPF1, error) {
	if config.Channels == 0 || config.SampleRate == 0 {
		return nil, ErrInvalidArgs
	}
	f := &LPF1{format: config.Format, channels: config.Channels, r1: make([]float64, config.Channels)}
	if err := f.Reinit(config); err != nil {
		return nil, err
	}
	return f, nil
}

// Reinit mirrors ma_lpf1_reinit.
func (f *LPF1) Reinit(config LPF1Config) error {
	if config.Channels != f.channels {
		return ErrInvalidOperation
	}
	f.a = math.Exp(-2 * math.Pi * config.CutoffFrequency / float64(config.SampleRate))
	return nil
}

// LatencyInFrames mirrors ma_lpf1_get_latency: 1 frame.
func (f *LPF1) LatencyInFrames() uint32 { return 1 }

// ProcessF32 processes interleaved f32 frames, in-place capable.
func (f *LPF1) ProcessF32(dst, src []float32, frameCount uint64) error {
	ch := int(f.channels)
	n := int(frameCount)
	if len(src) < n*ch || len(dst) < n*ch {
		return ErrInvalidArgs
	}
	a := f.a
	b := 1 - a
	for i := 0; i < n; i++ {
		for c := 0; c < ch; c++ {
			x := float64(src[i*ch+c])
			y := b*x + a*f.r1[c]
			dst[i*ch+c] = float32(y)
			f.r1[c] = y
		}
	}
	return nil
}

// Process mirrors ma_lpf1_process_pcm_frames.
func (f *LPF1) Process(dst, src []byte, frameCount uint64) error {
	switch f.format {
	case FormatF32:
		return f.ProcessF32(bytesToF32(dst), bytesToF32(src), frameCount)
	case FormatS16:
		return f.scratch.process(dst, src, frameCount, f.channels, f.ProcessF32)
	default:
		return ErrInvalidOperation
	}
}

/**************************************************************************
Second order filters (RBJ cookbook)
**************************************************************************/

// rbjLowpass computes RBJ cookbook low-pass biquad coefficients.
func rbjLowpass(sampleRate uint32, cutoff, q float64) (b0, b1, b2, a0, a1, a2 float64) {
	q = clampQ(q)
	w := 2 * math.Pi * cutoff / float64(sampleRate)
	s, c := math.Sin(w), math.Cos(w)
	alpha := s / (2 * q)
	b0 = (1 - c) / 2
	b1 = 1 - c
	b2 = (1 - c) / 2
	a0 = 1 + alpha
	a1 = -2 * c
	a2 = 1 - alpha
	return
}

func rbjHighpass(sampleRate uint32, cutoff, q float64) (b0, b1, b2, a0, a1, a2 float64) {
	q = clampQ(q)
	w := 2 * math.Pi * cutoff / float64(sampleRate)
	s, c := math.Sin(w), math.Cos(w)
	alpha := s / (2 * q)
	b0 = (1 + c) / 2
	b1 = -(1 + c)
	b2 = (1 + c) / 2
	a0 = 1 + alpha
	a1 = -2 * c
	a2 = 1 - alpha
	return
}

func rbjBandpass(sampleRate uint32, cutoff, q float64) (b0, b1, b2, a0, a1, a2 float64) {
	q = clampQ(q)
	w := 2 * math.Pi * cutoff / float64(sampleRate)
	s, c := math.Sin(w), math.Cos(w)
	alpha := s / (2 * q)
	b0 = alpha
	b1 = 0
	b2 = -alpha
	a0 = 1 + alpha
	a1 = -2 * c
	a2 = 1 - alpha
	return
}

func rbjNotch(sampleRate uint32, cutoff, q float64) (b0, b1, b2, a0, a1, a2 float64) {
	q = clampQ(q)
	w := 2 * math.Pi * cutoff / float64(sampleRate)
	s, c := math.Sin(w), math.Cos(w)
	alpha := s / (2 * q)
	b0 = 1
	b1 = -2 * c
	b2 = 1
	a0 = 1 + alpha
	a1 = -2 * c
	a2 = 1 - alpha
	return
}

func rbjPeak(sampleRate uint32, cutoff, gainDB, q float64) (b0, b1, b2, a0, a1, a2 float64) {
	q = clampQ(q)
	w := 2 * math.Pi * cutoff / float64(sampleRate)
	s, c := math.Sin(w), math.Cos(w)
	alpha := s / (2 * q)
	a := math.Pow(10, gainDB/40)
	b0 = 1 + alpha*a
	b1 = -2 * c
	b2 = 1 - alpha*a
	a0 = 1 + alpha/a
	a1 = -2 * c
	a2 = 1 - alpha/a
	return
}

func rbjLowShelf(sampleRate uint32, cutoff, gainDB, shelfSlope float64) (b0, b1, b2, a0, a1, a2 float64) {
	w := 2 * math.Pi * cutoff / float64(sampleRate)
	s, c := math.Sin(w), math.Cos(w)
	a := math.Pow(10, gainDB/40)
	alpha := s / 2 * math.Sqrt((a+1/a)*(1/shelfSlope-1)+2)
	sq := 2 * math.Sqrt(a) * alpha
	b0 = a * ((a + 1) - (a-1)*c + sq)
	b1 = 2 * a * ((a - 1) - (a+1)*c)
	b2 = a * ((a + 1) - (a-1)*c - sq)
	a0 = (a + 1) + (a-1)*c + sq
	a1 = -2 * ((a - 1) + (a+1)*c)
	a2 = (a + 1) + (a-1)*c - sq
	return
}

func rbjHighShelf(sampleRate uint32, cutoff, gainDB, shelfSlope float64) (b0, b1, b2, a0, a1, a2 float64) {
	w := 2 * math.Pi * cutoff / float64(sampleRate)
	s, c := math.Sin(w), math.Cos(w)
	a := math.Pow(10, gainDB/40)
	alpha := s / 2 * math.Sqrt((a+1/a)*(1/shelfSlope-1)+2)
	sq := 2 * math.Sqrt(a) * alpha
	b0 = a * ((a + 1) + (a-1)*c + sq)
	b1 = -2 * a * ((a - 1) + (a+1)*c)
	b2 = a * ((a + 1) + (a-1)*c - sq)
	a0 = (a + 1) - (a-1)*c + sq
	a1 = 2 * ((a - 1) - (a+1)*c)
	a2 = (a + 1) - (a-1)*c - sq
	return
}

func clampQ(q float64) float64 {
	if q <= 0 {
		return DefaultFilterQ
	}
	return q
}

// biquadFilter is the shared implementation for the RBJ-based second order
// filters (LPF2, HPF2, BPF2, Notch2, PeakEQ2, LoShelf2, HiShelf2).
type biquadFilter struct {
	bq *Biquad
}

func newBiquadFilter(format Format, channels uint32, b0, b1, b2, a0, a1, a2 float64) (*biquadFilter, error) {
	bq, err := NewBiquad(BiquadConfigInit(format, channels, b0, b1, b2, a0, a1, a2))
	if err != nil {
		return nil, err
	}
	return &biquadFilter{bq: bq}, nil
}

func (f *biquadFilter) reinit(b0, b1, b2, a0, a1, a2 float64) error {
	return f.bq.Reinit(BiquadConfig{Format: f.bq.format, Channels: f.bq.channels, B0: b0, B1: b1, B2: b2, A0: a0, A1: a1, A2: a2})
}

// LPF2 mirrors ma_lpf2.
type LPF2 struct{ biquadFilter }

// NewLPF2 mirrors ma_lpf2_init.
func NewLPF2(config LPF2Config) (*LPF2, error) {
	b0, b1, b2, a0, a1, a2 := rbjLowpass(config.SampleRate, config.CutoffFrequency, config.Q)
	f, err := newBiquadFilter(config.Format, config.Channels, b0, b1, b2, a0, a1, a2)
	if err != nil {
		return nil, err
	}
	return &LPF2{*f}, nil
}

// Reinit mirrors ma_lpf2_reinit.
func (f *LPF2) Reinit(config LPF2Config) error {
	b0, b1, b2, a0, a1, a2 := rbjLowpass(config.SampleRate, config.CutoffFrequency, config.Q)
	return f.reinit(b0, b1, b2, a0, a1, a2)
}

// Process processes frames in the configured format.
func (f *biquadFilter) Process(dst, src []byte, frameCount uint64) error {
	return f.bq.Process(dst, src, frameCount)
}

// ProcessF32 processes interleaved f32 frames.
func (f *biquadFilter) ProcessF32(dst, src []float32, frameCount uint64) error {
	return f.bq.ProcessF32(dst, src, frameCount)
}

// LatencyInFrames mirrors the second-order filter latency: 2 frames.
func (f *biquadFilter) LatencyInFrames() uint32 { return 2 }

// HPF1Config / HPF2Config mirror ma_hpf1_config / ma_hpf2_config.
type HPF1Config = LPF1Config
type HPF2Config = LPF1Config

// HPF1ConfigInit mirrors ma_hpf1_config_init.
func HPF1ConfigInit(format Format, channels, sampleRate uint32, cutoffFrequency float64) HPF1Config {
	return HPF1Config{Format: format, Channels: channels, SampleRate: sampleRate, CutoffFrequency: cutoffFrequency}
}

// HPF2ConfigInit mirrors ma_hpf2_config_init.
func HPF2ConfigInit(format Format, channels, sampleRate uint32, cutoffFrequency, q float64) HPF2Config {
	return HPF2Config{Format: format, Channels: channels, SampleRate: sampleRate, CutoffFrequency: cutoffFrequency, Q: q}
}

// HPF1 mirrors ma_hpf1: first order high-pass.
type HPF1 struct {
	format   Format
	channels uint32
	a        float64
	r1       []float64
	scratch  s16Scratch
}

// NewHPF1 mirrors ma_hpf1_init.
func NewHPF1(config HPF1Config) (*HPF1, error) {
	if config.Channels == 0 || config.SampleRate == 0 {
		return nil, ErrInvalidArgs
	}
	f := &HPF1{format: config.Format, channels: config.Channels, r1: make([]float64, config.Channels)}
	if err := f.Reinit(config); err != nil {
		return nil, err
	}
	return f, nil
}

// Reinit mirrors ma_hpf1_reinit.
func (f *HPF1) Reinit(config HPF1Config) error {
	if config.Channels != f.channels {
		return ErrInvalidOperation
	}
	f.a = math.Exp(-2 * math.Pi * config.CutoffFrequency / float64(config.SampleRate))
	return nil
}

// LatencyInFrames mirrors ma_hpf1_get_latency: 1 frame.
func (f *HPF1) LatencyInFrames() uint32 { return 1 }

// ProcessF32 processes interleaved f32 frames, in-place capable. The state
// holds the previous input sample, matching miniaudio's leaky differentiator.
func (f *HPF1) ProcessF32(dst, src []float32, frameCount uint64) error {
	ch := int(f.channels)
	n := int(frameCount)
	if len(src) < n*ch || len(dst) < n*ch {
		return ErrInvalidArgs
	}
	a := 1 - f.a
	b := 1 - a
	for i := 0; i < n; i++ {
		for c := 0; c < ch; c++ {
			x := float64(src[i*ch+c])
			y := b*x - a*f.r1[c]
			dst[i*ch+c] = float32(y)
			f.r1[c] = x
		}
	}
	return nil
}

// Process mirrors ma_hpf1_process_pcm_frames.
func (f *HPF1) Process(dst, src []byte, frameCount uint64) error {
	switch f.format {
	case FormatF32:
		return f.ProcessF32(bytesToF32(dst), bytesToF32(src), frameCount)
	case FormatS16:
		return f.scratch.process(dst, src, frameCount, f.channels, f.ProcessF32)
	default:
		return ErrInvalidOperation
	}
}

// HPF2 mirrors ma_hpf2.
type HPF2 struct{ biquadFilter }

// NewHPF2 mirrors ma_hpf2_init.
func NewHPF2(config HPF2Config) (*HPF2, error) {
	b0, b1, b2, a0, a1, a2 := rbjHighpass(config.SampleRate, config.CutoffFrequency, config.Q)
	f, err := newBiquadFilter(config.Format, config.Channels, b0, b1, b2, a0, a1, a2)
	if err != nil {
		return nil, err
	}
	return &HPF2{*f}, nil
}

// Reinit mirrors ma_hpf2_reinit.
func (f *HPF2) Reinit(config HPF2Config) error {
	b0, b1, b2, a0, a1, a2 := rbjHighpass(config.SampleRate, config.CutoffFrequency, config.Q)
	return f.reinit(b0, b1, b2, a0, a1, a2)
}

/**************************************************************************
Nth order cascades (LPF, HPF, BPF)
**************************************************************************/

// LPFConfig mirrors ma_lpf_config.
type LPFConfig struct {
	Format          Format
	Channels        uint32
	SampleRate      uint32
	CutoffFrequency float64
	Order           uint32 // 0 = passthrough; max MaxFilterOrder.
}

// LPFConfigInit mirrors ma_lpf_config_init.
func LPFConfigInit(format Format, channels, sampleRate uint32, cutoffFrequency float64, order uint32) LPFConfig {
	return LPFConfig{Format: format, Channels: channels, SampleRate: sampleRate, CutoffFrequency: cutoffFrequency, Order: order}
}

// HPFConfig mirrors ma_hpf_config.
type HPFConfig = LPFConfig

// HPFConfigInit mirrors ma_hpf_config_init.
func HPFConfigInit(format Format, channels, sampleRate uint32, cutoffFrequency float64, order uint32) HPFConfig {
	return HPFConfig{Format: format, Channels: channels, SampleRate: sampleRate, CutoffFrequency: cutoffFrequency, Order: order}
}

// LPF mirrors ma_lpf: an Nth order Butterworth low-pass built from cascaded
// first and second order sections.
type LPF struct {
	format     Format
	channels   uint32
	sampleRate uint32
	order      uint32
	lpf1       []*LPF1
	lpf2       []*LPF2
	scratch    s16Scratch
}

// NewLPF mirrors ma_lpf_init.
func NewLPF(config LPFConfig) (*LPF, error) {
	if config.Channels == 0 || config.SampleRate == 0 || config.Order > MaxFilterOrder {
		return nil, ErrInvalidArgs
	}
	f := &LPF{format: config.Format, channels: config.Channels}
	if err := f.Reinit(config); err != nil {
		return nil, err
	}
	return f, nil
}

// Reinit mirrors ma_lpf_reinit. The order must not change after init.
func (f *LPF) Reinit(config LPFConfig) error {
	if config.Channels != f.channels {
		return ErrInvalidOperation
	}
	if f.lpf1 != nil || f.lpf2 != nil {
		if config.Order != f.order {
			return ErrInvalidOperation
		}
	}
	lpf1Count := config.Order % 2
	lpf2Count := config.Order / 2

	if f.lpf1 == nil {
		f.lpf1 = make([]*LPF1, 0, lpf1Count)
		f.lpf2 = make([]*LPF2, 0, lpf2Count)
		for i := uint32(0); i < lpf1Count; i++ {
			sub, err := NewLPF1(LPF1ConfigInit(config.Format, config.Channels, config.SampleRate, config.CutoffFrequency))
			if err != nil {
				return err
			}
			f.lpf1 = append(f.lpf1, sub)
		}
		for i := uint32(0); i < lpf2Count; i++ {
			q := butterworthQ(config.Order, i)
			sub, err := NewLPF2(LPF2ConfigInit(config.Format, config.Channels, config.SampleRate, config.CutoffFrequency, q))
			if err != nil {
				return err
			}
			f.lpf2 = append(f.lpf2, sub)
		}
	} else {
		for _, sub := range f.lpf1 {
			if err := sub.Reinit(LPF1ConfigInit(config.Format, config.Channels, config.SampleRate, config.CutoffFrequency)); err != nil {
				return err
			}
		}
		for i, sub := range f.lpf2 {
			q := butterworthQ(config.Order, uint32(i))
			if err := sub.Reinit(LPF2ConfigInit(config.Format, config.Channels, config.SampleRate, config.CutoffFrequency, q)); err != nil {
				return err
			}
		}
	}

	f.order = config.Order
	f.sampleRate = config.SampleRate
	return nil
}

// butterworthQ returns the Q for the i-th second order section of an Nth
// order Butterworth cascade, matching miniaudio's pole placement.
func butterworthQ(order, i uint32) float64 {
	if order == 0 {
		return DefaultFilterQ
	}
	if order%2 == 0 {
		// Even order: angles (2i+1)*pi/(2N).
		a := float64(2*i+1) * math.Pi / float64(2*order)
		return 1 / (2 * math.Cos(a))
	}
	// Odd order: one first-order section plus pairs at angles (i+1)*pi/N.
	a := float64(i+1) * math.Pi / float64(order)
	return 1 / (2 * math.Cos(a))
}

// LatencyInFrames mirrors ma_lpf_get_latency.
func (f *LPF) LatencyInFrames() uint32 {
	return uint32(len(f.lpf1))*1 + uint32(len(f.lpf2))*2
}

// ProcessF32 processes interleaved f32 frames, in-place capable.
func (f *LPF) ProcessF32(dst, src []float32, frameCount uint64) error {
	cur := src
	for _, sub := range f.lpf1 {
		if err := sub.ProcessF32(dst, cur, frameCount); err != nil {
			return err
		}
		cur = dst
	}
	for _, sub := range f.lpf2 {
		if err := sub.ProcessF32(dst, cur, frameCount); err != nil {
			return err
		}
		cur = dst
	}
	if len(f.lpf1) == 0 && len(f.lpf2) == 0 {
		copy(dst[:frameCount*uint64(f.channels)], src)
	}
	return nil
}

// Process mirrors ma_lpf_process_pcm_frames.
func (f *LPF) Process(dst, src []byte, frameCount uint64) error {
	switch f.format {
	case FormatF32:
		return f.ProcessF32(bytesToF32(dst), bytesToF32(src), frameCount)
	case FormatS16:
		return f.scratch.process(dst, src, frameCount, f.channels, f.ProcessF32)
	default:
		return ErrInvalidOperation
	}
}

// HPF mirrors ma_hpf: an Nth order Butterworth high-pass cascade.
type HPF struct {
	format   Format
	channels uint32
	order    uint32
	hpf1     []*HPF1
	hpf2     []*HPF2
	scratch  s16Scratch
}

// NewHPF mirrors ma_hpf_init.
func NewHPF(config HPFConfig) (*HPF, error) {
	if config.Channels == 0 || config.SampleRate == 0 || config.Order > MaxFilterOrder {
		return nil, ErrInvalidArgs
	}
	f := &HPF{format: config.Format, channels: config.Channels}
	if err := f.Reinit(config); err != nil {
		return nil, err
	}
	return f, nil
}

// Reinit mirrors ma_hpf_reinit.
func (f *HPF) Reinit(config HPFConfig) error {
	if config.Channels != f.channels {
		return ErrInvalidOperation
	}
	if (f.hpf1 != nil || f.hpf2 != nil) && config.Order != f.order {
		return ErrInvalidOperation
	}
	hpf1Count := config.Order % 2
	hpf2Count := config.Order / 2

	if f.hpf1 == nil {
		for i := uint32(0); i < hpf1Count; i++ {
			sub, err := NewHPF1(HPF1ConfigInit(config.Format, config.Channels, config.SampleRate, config.CutoffFrequency))
			if err != nil {
				return err
			}
			f.hpf1 = append(f.hpf1, sub)
		}
		for i := uint32(0); i < hpf2Count; i++ {
			q := butterworthQ(config.Order, i)
			sub, err := NewHPF2(HPF2ConfigInit(config.Format, config.Channels, config.SampleRate, config.CutoffFrequency, q))
			if err != nil {
				return err
			}
			f.hpf2 = append(f.hpf2, sub)
		}
	} else {
		for _, sub := range f.hpf1 {
			if err := sub.Reinit(HPF1ConfigInit(config.Format, config.Channels, config.SampleRate, config.CutoffFrequency)); err != nil {
				return err
			}
		}
		for i, sub := range f.hpf2 {
			q := butterworthQ(config.Order, uint32(i))
			if err := sub.Reinit(HPF2ConfigInit(config.Format, config.Channels, config.SampleRate, config.CutoffFrequency, q)); err != nil {
				return err
			}
		}
	}
	f.order = config.Order
	return nil
}

// LatencyInFrames mirrors ma_hpf_get_latency.
func (f *HPF) LatencyInFrames() uint32 {
	return uint32(len(f.hpf1))*1 + uint32(len(f.hpf2))*2
}

// ProcessF32 processes interleaved f32 frames, in-place capable.
func (f *HPF) ProcessF32(dst, src []float32, frameCount uint64) error {
	cur := src
	for _, sub := range f.hpf1 {
		if err := sub.ProcessF32(dst, cur, frameCount); err != nil {
			return err
		}
		cur = dst
	}
	for _, sub := range f.hpf2 {
		if err := sub.ProcessF32(dst, cur, frameCount); err != nil {
			return err
		}
		cur = dst
	}
	if len(f.hpf1) == 0 && len(f.hpf2) == 0 {
		copy(dst[:frameCount*uint64(f.channels)], src)
	}
	return nil
}

// Process mirrors ma_hpf_process_pcm_frames.
func (f *HPF) Process(dst, src []byte, frameCount uint64) error {
	switch f.format {
	case FormatF32:
		return f.ProcessF32(bytesToF32(dst), bytesToF32(src), frameCount)
	case FormatS16:
		return f.scratch.process(dst, src, frameCount, f.channels, f.ProcessF32)
	default:
		return ErrInvalidOperation
	}
}

// BPF2Config mirrors ma_bpf2_config.
type BPF2Config = LPF2Config

// BPF2ConfigInit mirrors ma_bpf2_config_init.
func BPF2ConfigInit(format Format, channels, sampleRate uint32, cutoffFrequency, q float64) BPF2Config {
	return BPF2Config{Format: format, Channels: channels, SampleRate: sampleRate, CutoffFrequency: cutoffFrequency, Q: q}
}

// BPF2 mirrors ma_bpf2.
type BPF2 struct{ biquadFilter }

// NewBPF2 mirrors ma_bpf2_init.
func NewBPF2(config BPF2Config) (*BPF2, error) {
	b0, b1, b2, a0, a1, a2 := rbjBandpass(config.SampleRate, config.CutoffFrequency, config.Q)
	f, err := newBiquadFilter(config.Format, config.Channels, b0, b1, b2, a0, a1, a2)
	if err != nil {
		return nil, err
	}
	return &BPF2{*f}, nil
}

// Reinit mirrors ma_bpf2_reinit.
func (f *BPF2) Reinit(config BPF2Config) error {
	b0, b1, b2, a0, a1, a2 := rbjBandpass(config.SampleRate, config.CutoffFrequency, config.Q)
	return f.reinit(b0, b1, b2, a0, a1, a2)
}

// BPFConfig mirrors ma_bpf_config. Order must be even (each 2 orders is one
// second-order section).
type BPFConfig struct {
	Format          Format
	Channels        uint32
	SampleRate      uint32
	CutoffFrequency float64
	Order           uint32
}

// BPFConfigInit mirrors ma_bpf_config_init.
func BPFConfigInit(format Format, channels, sampleRate uint32, cutoffFrequency float64, order uint32) BPFConfig {
	return BPFConfig{Format: format, Channels: channels, SampleRate: sampleRate, CutoffFrequency: cutoffFrequency, Order: order}
}

// BPF mirrors ma_bpf.
type BPF struct {
	format   Format
	channels uint32
	order    uint32
	bpf2     []*BPF2
	scratch  s16Scratch
}

// NewBPF mirrors ma_bpf_init.
func NewBPF(config BPFConfig) (*BPF, error) {
	if config.Channels == 0 || config.SampleRate == 0 || config.Order > MaxFilterOrder || config.Order%2 != 0 {
		return nil, ErrInvalidArgs
	}
	f := &BPF{format: config.Format, channels: config.Channels}
	if err := f.Reinit(config); err != nil {
		return nil, err
	}
	return f, nil
}

// Reinit mirrors ma_bpf_reinit.
func (f *BPF) Reinit(config BPFConfig) error {
	if config.Channels != f.channels || config.Order%2 != 0 {
		return ErrInvalidOperation
	}
	if f.bpf2 != nil && config.Order != f.order {
		return ErrInvalidOperation
	}
	count := config.Order / 2
	if f.bpf2 == nil {
		for i := uint32(0); i < count; i++ {
			sub, err := NewBPF2(BPF2ConfigInit(config.Format, config.Channels, config.SampleRate, config.CutoffFrequency, DefaultFilterQ))
			if err != nil {
				return err
			}
			f.bpf2 = append(f.bpf2, sub)
		}
	} else {
		for _, sub := range f.bpf2 {
			if err := sub.Reinit(BPF2ConfigInit(config.Format, config.Channels, config.SampleRate, config.CutoffFrequency, DefaultFilterQ)); err != nil {
				return err
			}
		}
	}
	f.order = config.Order
	return nil
}

// LatencyInFrames mirrors ma_bpf_get_latency.
func (f *BPF) LatencyInFrames() uint32 { return uint32(len(f.bpf2)) * 2 }

// ProcessF32 processes interleaved f32 frames, in-place capable.
func (f *BPF) ProcessF32(dst, src []float32, frameCount uint64) error {
	cur := src
	for _, sub := range f.bpf2 {
		if err := sub.ProcessF32(dst, cur, frameCount); err != nil {
			return err
		}
		cur = dst
	}
	if len(f.bpf2) == 0 {
		copy(dst[:frameCount*uint64(f.channels)], src)
	}
	return nil
}

// Process mirrors ma_bpf_process_pcm_frames.
func (f *BPF) Process(dst, src []byte, frameCount uint64) error {
	switch f.format {
	case FormatF32:
		return f.ProcessF32(bytesToF32(dst), bytesToF32(src), frameCount)
	case FormatS16:
		return f.scratch.process(dst, src, frameCount, f.channels, f.ProcessF32)
	default:
		return ErrInvalidOperation
	}
}

/**************************************************************************
Notch, peaking EQ and shelf filters
**************************************************************************/

// NotchConfig mirrors ma_notch2_config.
type NotchConfig struct {
	Format     Format
	Channels   uint32
	SampleRate uint32
	Q          float64
	Frequency  float64
}

// Notch2ConfigInit mirrors ma_notch2_config_init.
func Notch2ConfigInit(format Format, channels, sampleRate uint32, q, frequency float64) NotchConfig {
	return NotchConfig{Format: format, Channels: channels, SampleRate: sampleRate, Q: q, Frequency: frequency}
}

// Notch2 mirrors ma_notch2.
type Notch2 struct{ biquadFilter }

// NewNotch2 mirrors ma_notch2_init.
func NewNotch2(config NotchConfig) (*Notch2, error) {
	b0, b1, b2, a0, a1, a2 := rbjNotch(config.SampleRate, config.Frequency, config.Q)
	f, err := newBiquadFilter(config.Format, config.Channels, b0, b1, b2, a0, a1, a2)
	if err != nil {
		return nil, err
	}
	return &Notch2{*f}, nil
}

// Reinit mirrors ma_notch2_reinit.
func (f *Notch2) Reinit(config NotchConfig) error {
	b0, b1, b2, a0, a1, a2 := rbjNotch(config.SampleRate, config.Frequency, config.Q)
	return f.reinit(b0, b1, b2, a0, a1, a2)
}

// PeakConfig mirrors ma_peak2_config.
type PeakConfig struct {
	Format     Format
	Channels   uint32
	SampleRate uint32
	GainDB     float64
	Q          float64
	Frequency  float64
}

// Peak2ConfigInit mirrors ma_peak2_config_init.
func Peak2ConfigInit(format Format, channels, sampleRate uint32, gainDB, q, frequency float64) PeakConfig {
	return PeakConfig{Format: format, Channels: channels, SampleRate: sampleRate, GainDB: gainDB, Q: q, Frequency: frequency}
}

// Peak2 mirrors ma_peak2: a peaking EQ filter.
type Peak2 struct{ biquadFilter }

// NewPeak2 mirrors ma_peak2_init.
func NewPeak2(config PeakConfig) (*Peak2, error) {
	b0, b1, b2, a0, a1, a2 := rbjPeak(config.SampleRate, config.Frequency, config.GainDB, config.Q)
	f, err := newBiquadFilter(config.Format, config.Channels, b0, b1, b2, a0, a1, a2)
	if err != nil {
		return nil, err
	}
	return &Peak2{*f}, nil
}

// Reinit mirrors ma_peak2_reinit.
func (f *Peak2) Reinit(config PeakConfig) error {
	b0, b1, b2, a0, a1, a2 := rbjPeak(config.SampleRate, config.Frequency, config.GainDB, config.Q)
	return f.reinit(b0, b1, b2, a0, a1, a2)
}

// ShelfConfig mirrors ma_loshelf2_config / ma_hishelf2_config.
type ShelfConfig struct {
	Format     Format
	Channels   uint32
	SampleRate uint32
	GainDB     float64
	ShelfSlope float64
	Frequency  float64
}

// LoshelfConfigInit mirrors ma_loshelf2_config_init.
func LoshelfConfigInit(format Format, channels, sampleRate uint32, gainDB, shelfSlope, frequency float64) ShelfConfig {
	return ShelfConfig{Format: format, Channels: channels, SampleRate: sampleRate, GainDB: gainDB, ShelfSlope: shelfSlope, Frequency: frequency}
}

// HishelfConfigInit mirrors ma_hishelf2_config_init.
func HishelfConfigInit(format Format, channels, sampleRate uint32, gainDB, shelfSlope, frequency float64) ShelfConfig {
	return ShelfConfig{Format: format, Channels: channels, SampleRate: sampleRate, GainDB: gainDB, ShelfSlope: shelfSlope, Frequency: frequency}
}

// Loshelf2 mirrors ma_loshelf2: a low shelf filter.
type Loshelf2 struct{ biquadFilter }

// NewLoshelf2 mirrors ma_loshelf2_init.
func NewLoshelf2(config ShelfConfig) (*Loshelf2, error) {
	slope := config.ShelfSlope
	if slope <= 0 {
		slope = 1
	}
	b0, b1, b2, a0, a1, a2 := rbjLowShelf(config.SampleRate, config.Frequency, config.GainDB, slope)
	f, err := newBiquadFilter(config.Format, config.Channels, b0, b1, b2, a0, a1, a2)
	if err != nil {
		return nil, err
	}
	return &Loshelf2{*f}, nil
}

// Reinit mirrors ma_loshelf2_reinit.
func (f *Loshelf2) Reinit(config ShelfConfig) error {
	slope := config.ShelfSlope
	if slope <= 0 {
		slope = 1
	}
	b0, b1, b2, a0, a1, a2 := rbjLowShelf(config.SampleRate, config.Frequency, config.GainDB, slope)
	return f.reinit(b0, b1, b2, a0, a1, a2)
}

// Hishelf2 mirrors ma_hishelf2: a high shelf filter.
type Hishelf2 struct{ biquadFilter }

// NewHishelf2 mirrors ma_hishelf2_init.
func NewHishelf2(config ShelfConfig) (*Hishelf2, error) {
	slope := config.ShelfSlope
	if slope <= 0 {
		slope = 1
	}
	b0, b1, b2, a0, a1, a2 := rbjHighShelf(config.SampleRate, config.Frequency, config.GainDB, slope)
	f, err := newBiquadFilter(config.Format, config.Channels, b0, b1, b2, a0, a1, a2)
	if err != nil {
		return nil, err
	}
	return &Hishelf2{*f}, nil
}

// Reinit mirrors ma_hishelf2_reinit.
func (f *Hishelf2) Reinit(config ShelfConfig) error {
	slope := config.ShelfSlope
	if slope <= 0 {
		slope = 1
	}
	b0, b1, b2, a0, a1, a2 := rbjHighShelf(config.SampleRate, config.Frequency, config.GainDB, slope)
	return f.reinit(b0, b1, b2, a0, a1, a2)
}
