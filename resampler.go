package gominiaudio

// ResampleAlgorithm mirrors ma_resample_algorithm.
type ResampleAlgorithm uint32

const (
	ResampleAlgorithmLinear ResampleAlgorithm = 0 // Fastest, lowest quality. Optional low-pass filtering. Default.
	ResampleAlgorithmCustom ResampleAlgorithm = 1
)

// DefaultResamplerLPFOrder mirrors MA_DEFAULT_RESAMPLER_LPF_ORDER.
const DefaultResamplerLPFOrder = 4

// LinearResamplerConfig mirrors ma_linear_resampler_config.
type LinearResamplerConfig struct {
	Format           Format // FormatF32 or FormatS16.
	Channels         uint32
	SampleRateIn     uint32
	SampleRateOut    uint32
	LPFOrder         uint32  // How many low-pass filter stages to apply. 0 = no filtering. Max MaxFilterOrder.
	LPFNyquistFactor float64 // 0..1. Defaults to 1. 1 = Half the sampling frequency (Nyquist Frequency).
}

// LinearResamplerConfigInit mirrors ma_linear_resampler_config_init.
func LinearResamplerConfigInit(format Format, channels, sampleRateIn, sampleRateOut uint32) LinearResamplerConfig {
	return LinearResamplerConfig{
		Format:           format,
		Channels:         channels,
		SampleRateIn:     sampleRateIn,
		SampleRateOut:    sampleRateOut,
		LPFOrder:         min(DefaultResamplerLPFOrder, MaxFilterOrder),
		LPFNyquistFactor: 1,
	}
}

// LinearResampler mirrors ma_linear_resampler: linear interpolation with
// optional Butterworth low-pass filtering. All internal processing is f32;
// s16 buffers are converted at the edges using a reusable scratch buffer so
// the audio path stays allocation-free.
type LinearResampler struct {
	config LinearResamplerConfig

	// Simplified (GCD-reduced) rates used for timing.
	sampleRateIn  uint32
	sampleRateOut uint32

	inAdvanceInt  uint32
	inAdvanceFrac uint32
	inTimeInt     uint32
	inTimeFrac    uint32

	x0, x1 []float32 // Previous and next input frames, one sample per channel.
	lpf    *LPF

	scratchIn  []float32
	scratchOut []float32
	lpfScratch []float32 // Block-filtered input staging (downsampling path).
}

// NewLinearResampler mirrors ma_linear_resampler_init.
func NewLinearResampler(config LinearResamplerConfig) (*LinearResampler, error) {
	if config.Channels == 0 || config.Channels > MaxChannels {
		return nil, ErrInvalidArgs
	}
	if config.SampleRateIn == 0 || config.SampleRateOut == 0 {
		return nil, ErrInvalidArgs
	}
	if config.Format != FormatF32 && config.Format != FormatS16 {
		return nil, ErrInvalidArgs
	}
	if config.LPFOrder > MaxFilterOrder {
		return nil, ErrInvalidArgs
	}
	if config.LPFNyquistFactor <= 0 {
		config.LPFNyquistFactor = 1
	}

	r := &LinearResampler{
		config: config,
		x0:     make([]float32, config.Channels),
		x1:     make([]float32, config.Channels),
	}
	if err := r.setRateInternal(config.SampleRateIn, config.SampleRateOut, true); err != nil {
		return nil, err
	}
	return r, nil
}

func gcdU32(a, b uint32) uint32 {
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}

func (r *LinearResampler) setRateInternal(sampleRateIn, sampleRateOut uint32, isInit bool) error {
	if sampleRateIn == 0 || sampleRateOut == 0 {
		return ErrInvalidArgs
	}
	r.config.SampleRateIn = sampleRateIn
	r.config.SampleRateOut = sampleRateOut

	gcd := gcdU32(sampleRateIn, sampleRateOut)
	r.sampleRateIn = sampleRateIn / gcd
	r.sampleRateOut = sampleRateOut / gcd

	if r.config.LPFOrder > 0 {
		lpfSampleRate := max(sampleRateIn, sampleRateOut)
		lpfCutoff := float64(min(sampleRateIn, sampleRateOut)) * 0.5 * r.config.LPFNyquistFactor
		lpfConfig := LPFConfigInit(FormatF32, r.config.Channels, lpfSampleRate, lpfCutoff, r.config.LPFOrder)
		if r.lpf == nil {
			lpf, err := NewLPF(lpfConfig)
			if err != nil {
				return err
			}
			r.lpf = lpf
		} else {
			if err := r.lpf.Reinit(lpfConfig); err != nil {
				return err
			}
		}
	}

	r.inAdvanceInt = r.sampleRateIn / r.sampleRateOut
	r.inAdvanceFrac = r.sampleRateIn % r.sampleRateOut

	if isInit {
		// Prime the timer so the first output frame loads an input frame
		// into x1 before interpolating, mirroring miniaudio.
		r.inTimeInt = 1
		r.inTimeFrac = 0
	}
	return nil
}

// SetRate mirrors ma_linear_resampler_set_rate. It can be called while
// streaming for dynamic rate changes.
func (r *LinearResampler) SetRate(sampleRateIn, sampleRateOut uint32) error {
	return r.setRateInternal(sampleRateIn, sampleRateOut, false)
}

// SetRateRatio mirrors ma_linear_resampler_set_rate_ratio. The ratio is
// in/out.
func (r *LinearResampler) SetRateRatio(ratio float32) error {
	if ratio <= 0 {
		return ErrInvalidArgs
	}
	n := uint32(1000)
	d := uint32(float32(n) / ratio)
	if d == 0 {
		d = 1
	}
	return r.SetRate(n, d)
}

// Reset mirrors ma_linear_resampler_reset.
func (r *LinearResampler) Reset() {
	for i := range r.x0 {
		r.x0[i] = 0
		r.x1[i] = 0
	}
	r.inTimeInt = 1
	r.inTimeFrac = 0
	if r.lpf != nil {
		for _, s := range r.lpf.lpf1 {
			for i := range s.r1 {
				s.r1[i] = 0
			}
		}
		for _, s := range r.lpf.lpf2 {
			s.bq.Clear()
		}
	}
}

// InputLatencyInFrames mirrors ma_linear_resampler_get_input_latency.
func (r *LinearResampler) InputLatencyInFrames() uint64 {
	lat := uint64(1)
	if r.lpf != nil {
		lat += uint64(r.lpf.LatencyInFrames())
	}
	return lat
}

// OutputLatencyInFrames mirrors ma_linear_resampler_get_output_latency.
func (r *LinearResampler) OutputLatencyInFrames() uint64 {
	return r.InputLatencyInFrames() * uint64(r.sampleRateOut) / uint64(r.sampleRateIn)
}

// RequiredInputFrameCount mirrors
// ma_linear_resampler_get_required_input_frame_count: how many input frames
// are required to produce outputFrameCount output frames.
func (r *LinearResampler) RequiredInputFrameCount(outputFrameCount uint64) uint64 {
	if outputFrameCount == 0 {
		return 0
	}
	// Any whole input frames still pending consumption count first.
	count := uint64(r.inTimeInt)
	// Each subsequent output frame advances the input timer.
	frac := uint64(r.inTimeFrac)
	count += (outputFrameCount - 1) * uint64(r.inAdvanceInt)
	frac += (outputFrameCount - 1) * uint64(r.inAdvanceFrac)
	count += frac / uint64(r.sampleRateOut)
	return count
}

// ExpectedOutputFrameCount mirrors
// ma_linear_resampler_get_expected_output_frame_count: how many output
// frames can be produced from inputFrameCount input frames.
func (r *LinearResampler) ExpectedOutputFrameCount(inputFrameCount uint64) uint64 {
	// Simulate the timer advance.
	outputFrameCount := uint64(0)
	inTimeInt := uint64(r.inTimeInt)
	inTimeFrac := uint64(r.inTimeFrac)
	for {
		consumed := inTimeInt
		if consumed > inputFrameCount {
			break
		}
		inputFrameCount -= consumed
		inTimeInt = 0
		outputFrameCount++
		inTimeInt += uint64(r.inAdvanceInt)
		inTimeFrac += uint64(r.inAdvanceFrac)
		if inTimeFrac >= uint64(r.sampleRateOut) {
			inTimeFrac -= uint64(r.sampleRateOut)
			inTimeInt++
		}
	}
	return outputFrameCount
}

// ProcessF32 resamples interleaved f32 frames. src may be nil to feed
// silence. It returns the number of input frames consumed and output frames
// written.
func (r *LinearResampler) ProcessF32(dst, src []float32, frameCountIn, frameCountOut uint64) (framesIn, framesOut uint64, err error) {
	ch := int(r.config.Channels)
	if dst == nil {
		return 0, 0, ErrInvalidArgs
	}

	// LPF placement mirrors miniaudio: filter the input when downsampling,
	// the output when upsampling.
	downsampling := r.sampleRateIn > r.sampleRateOut

	// When downsampling, low-pass filter the input as one block up front
	// instead of frame-at-a-time inside the load loop. Input consumption is
	// deterministic - min(frameCountIn, RequiredInputFrameCount(out)) - so
	// exactly the frames that will be consumed are filtered, in the same
	// order the per-frame path would have filtered them.
	if r.lpf != nil && downsampling && frameCountOut > 0 {
		toFilter := r.RequiredInputFrameCount(frameCountOut)
		if toFilter > frameCountIn {
			toFilter = frameCountIn
		}
		if toFilter > 0 {
			need := int(toFilter) * ch
			if cap(r.lpfScratch) < need {
				r.lpfScratch = make([]float32, need)
			}
			filtered := r.lpfScratch[:need]
			if src != nil {
				_ = r.lpf.ProcessF32(filtered, src[:need], toFilter)
			} else {
				// Silence input: the filter state must still advance.
				for i := range filtered {
					filtered[i] = 0
				}
				_ = r.lpf.ProcessF32(filtered, filtered, toFilter)
			}
			src = filtered
			frameCountIn = toFilter
		}
	}

	var framesProcessedIn, framesProcessedOut uint64
	for framesProcessedOut < frameCountOut {
		// Load input frames until the timer says the next output frame sits
		// between x0 and x1.
		for r.inTimeInt > 0 && framesProcessedIn < frameCountIn {
			copy(r.x0, r.x1)
			base := int(framesProcessedIn) * ch
			if src != nil {
				// Pre-check eliminates per-iteration bounds check on src[base+c].
				_ = src[base+ch-1]
				for c := 0; c < ch; c++ {
					r.x1[c] = src[base+c]
				}
			} else {
				for c := 0; c < ch; c++ {
					r.x1[c] = 0
				}
			}
			framesProcessedIn++
			r.inTimeInt--
		}
		if r.inTimeInt > 0 {
			break // Ran out of input data.
		}

		// Interpolate. Mono and stereo are inlined: the per-frame call
		// through the lerpChannels dispatch variable costs more than the
		// two multiplies it performs.
		a := float32(r.inTimeFrac) / float32(r.sampleRateOut)
		outBase := int(framesProcessedOut) * ch
		switch ch {
		case 1:
			dst[outBase] = r.x0[0] + (r.x1[0]-r.x0[0])*a
		case 2:
			x0, x1 := r.x0, r.x1
			dst[outBase] = x0[0] + (x1[0]-x0[0])*a
			dst[outBase+1] = x0[1] + (x1[1]-x0[1])*a
		default:
			lerpChannels(dst[outBase:], r.x0, r.x1, a, ch)
		}

		framesProcessedOut++
		r.inTimeInt += r.inAdvanceInt
		r.inTimeFrac += r.inAdvanceFrac
		if r.inTimeFrac >= r.sampleRateOut {
			r.inTimeFrac -= r.sampleRateOut
			r.inTimeInt++
		}
	}

	// When upsampling, low-pass filter the generated output as one block.
	if r.lpf != nil && !downsampling && framesProcessedOut > 0 {
		n := int(framesProcessedOut) * ch
		_ = r.lpf.ProcessF32(dst[:n], dst[:n], framesProcessedOut)
	}

	return framesProcessedIn, framesProcessedOut, nil
}

// Process mirrors ma_linear_resampler_process_pcm_frames for the configured
// format. src may be nil to feed silence.
func (r *LinearResampler) Process(dst, src []byte, frameCountIn, frameCountOut uint64) (framesIn, framesOut uint64, err error) {
	switch r.config.Format {
	case FormatF32:
		var srcF []float32
		if src != nil {
			srcF = bytesToF32(src)
		}
		return r.ProcessF32(bytesToF32(dst), srcF, frameCountIn, frameCountOut)
	case FormatS16:
		ch := int(r.config.Channels)
		inSamples := int(frameCountIn) * ch
		outSamples := int(frameCountOut) * ch
		if cap(r.scratchIn) < inSamples {
			r.scratchIn = make([]float32, inSamples)
		}
		if cap(r.scratchOut) < outSamples {
			r.scratchOut = make([]float32, outSamples)
		}
		var srcF []float32
		if src != nil {
			srcF = r.scratchIn[:inSamples]
			if err := PCMConvert(f32ToBytes(srcF), FormatF32, src, FormatS16, uint64(inSamples), DitherModeNone); err != nil {
				return 0, 0, err
			}
		}
		dstF := r.scratchOut[:outSamples]
		fin, fout, err := r.ProcessF32(dstF, srcF, frameCountIn, frameCountOut)
		if err != nil {
			return fin, fout, err
		}
		writtenSamples := int(fout) * ch
		if err := PCMConvert(dst, FormatS16, f32ToBytes(dstF[:writtenSamples]), FormatF32, uint64(writtenSamples), DitherModeNone); err != nil {
			return fin, fout, err
		}
		return fin, fout, nil
	default:
		return 0, 0, ErrInvalidOperation
	}
}

/**************************************************************************
Generic resampler (ma_resampler)
**************************************************************************/

// ResamplingBackend is the interface for custom resampling algorithms,
// mirroring ma_resampling_backend_vtable.
type ResamplingBackend interface {
	Process(dst, src []byte, frameCountIn, frameCountOut uint64) (framesIn, framesOut uint64, err error)
	SetRate(sampleRateIn, sampleRateOut uint32) error
	InputLatencyInFrames() uint64
	OutputLatencyInFrames() uint64
	RequiredInputFrameCount(outputFrameCount uint64) uint64
	ExpectedOutputFrameCount(inputFrameCount uint64) uint64
	Reset()
}

// ResamplerConfig mirrors ma_resampler_config.
type ResamplerConfig struct {
	Format        Format // Must be either FormatF32 or FormatS16.
	Channels      uint32
	SampleRateIn  uint32
	SampleRateOut uint32
	Algorithm     ResampleAlgorithm
	Linear        struct {
		LPFOrder uint32
	}
	Backend ResamplingBackend // Used when Algorithm is ResampleAlgorithmCustom.
}

// ResamplerConfigInit mirrors ma_resampler_config_init.
func ResamplerConfigInit(format Format, channels, sampleRateIn, sampleRateOut uint32, algorithm ResampleAlgorithm) ResamplerConfig {
	c := ResamplerConfig{
		Format:        format,
		Channels:      channels,
		SampleRateIn:  sampleRateIn,
		SampleRateOut: sampleRateOut,
		Algorithm:     algorithm,
	}
	c.Linear.LPFOrder = min(DefaultResamplerLPFOrder, MaxFilterOrder)
	return c
}

// Resampler mirrors ma_resampler.
type Resampler struct {
	format        Format
	channels      uint32
	sampleRateIn  uint32
	sampleRateOut uint32
	backend       ResamplingBackend
}

// NewResampler mirrors ma_resampler_init.
func NewResampler(config ResamplerConfig) (*Resampler, error) {
	if config.Format != FormatF32 && config.Format != FormatS16 {
		return nil, ErrInvalidArgs
	}
	r := &Resampler{
		format:        config.Format,
		channels:      config.Channels,
		sampleRateIn:  config.SampleRateIn,
		sampleRateOut: config.SampleRateOut,
	}
	switch config.Algorithm {
	case ResampleAlgorithmLinear:
		lrc := LinearResamplerConfigInit(config.Format, config.Channels, config.SampleRateIn, config.SampleRateOut)
		lrc.LPFOrder = config.Linear.LPFOrder
		lr, err := NewLinearResampler(lrc)
		if err != nil {
			return nil, err
		}
		r.backend = lr
	case ResampleAlgorithmCustom:
		if config.Backend == nil {
			return nil, ErrInvalidArgs
		}
		r.backend = config.Backend
	default:
		return nil, ErrInvalidArgs
	}
	return r, nil
}

// Process mirrors ma_resampler_process_pcm_frames. src may be nil to feed
// silence.
func (r *Resampler) Process(dst, src []byte, frameCountIn, frameCountOut uint64) (framesIn, framesOut uint64, err error) {
	return r.backend.Process(dst, src, frameCountIn, frameCountOut)
}

// SetRate mirrors ma_resampler_set_rate.
func (r *Resampler) SetRate(sampleRateIn, sampleRateOut uint32) error {
	if err := r.backend.SetRate(sampleRateIn, sampleRateOut); err != nil {
		return err
	}
	r.sampleRateIn = sampleRateIn
	r.sampleRateOut = sampleRateOut
	return nil
}

// SetRateRatio mirrors ma_resampler_set_rate_ratio. The ratio is in/out.
func (r *Resampler) SetRateRatio(ratio float32) error {
	if lr, ok := r.backend.(*LinearResampler); ok {
		if err := lr.SetRateRatio(ratio); err != nil {
			return err
		}
		r.sampleRateIn = lr.config.SampleRateIn
		r.sampleRateOut = lr.config.SampleRateOut
		return nil
	}
	if ratio <= 0 {
		return ErrInvalidArgs
	}
	n := uint32(1000)
	d := uint32(float32(n) / ratio)
	if d == 0 {
		d = 1
	}
	return r.SetRate(n, d)
}

// RequiredInputFrameCount mirrors ma_resampler_get_required_input_frame_count.
func (r *Resampler) RequiredInputFrameCount(outputFrameCount uint64) uint64 {
	return r.backend.RequiredInputFrameCount(outputFrameCount)
}

// ExpectedOutputFrameCount mirrors ma_resampler_get_expected_output_frame_count.
func (r *Resampler) ExpectedOutputFrameCount(inputFrameCount uint64) uint64 {
	return r.backend.ExpectedOutputFrameCount(inputFrameCount)
}

// InputLatencyInFrames mirrors ma_resampler_get_input_latency.
func (r *Resampler) InputLatencyInFrames() uint64 { return r.backend.InputLatencyInFrames() }

// OutputLatencyInFrames mirrors ma_resampler_get_output_latency.
func (r *Resampler) OutputLatencyInFrames() uint64 { return r.backend.OutputLatencyInFrames() }

// Reset mirrors ma_resampler_reset.
func (r *Resampler) Reset() { r.backend.Reset() }

// SampleRateIn returns the input sample rate.
func (r *Resampler) SampleRateIn() uint32 { return r.sampleRateIn }

// SampleRateOut returns the output sample rate.
func (r *Resampler) SampleRateOut() uint32 { return r.sampleRateOut }

// lerpChannelsImpl is selected at init time; defaults to scalar.
var lerpChannelsImpl func(dst, x0, x1 []float32, a float32, ch int) = lerpChannelsScalar

// lerpChannels dispatches to the active implementation.
func lerpChannels(dst, x0, x1 []float32, a float32, ch int) {
	lerpChannelsImpl(dst, x0, x1, a, ch)
}

// lerpChannelsScalar computes dst[c] = x0[c] + (x1[c]-x0[c])*a for each channel.
func lerpChannelsScalar(dst, x0, x1 []float32, a float32, ch int) {
	for c := 0; c < ch; c++ {
		dst[c] = x0[c] + (x1[c]-x0[c])*a
	}
}
