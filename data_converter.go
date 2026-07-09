package gominiaudio

// DataConverterConfig mirrors ma_data_converter_config.
type DataConverterConfig struct {
	FormatIn                        Format
	FormatOut                       Format
	ChannelsIn                      uint32
	ChannelsOut                     uint32
	SampleRateIn                    uint32
	SampleRateOut                   uint32
	ChannelMapIn                    []Channel
	ChannelMapOut                   []Channel
	DitherMode                      DitherMode
	ChannelMixMode                  ChannelMixMode
	CalculateLFEFromSpatialChannels bool
	ChannelWeights                  [][]float32 // [ChannelsIn][ChannelsOut]. Custom weights for ChannelMixModeCustomWeights.
	AllowDynamicSampleRate          bool
	Resampling                      ResamplerConfig
}

// DataConverterConfigInit mirrors ma_data_converter_config_init.
func DataConverterConfigInit(formatIn, formatOut Format, channelsIn, channelsOut, sampleRateIn, sampleRateOut uint32) DataConverterConfig {
	c := DataConverterConfig{
		FormatIn:       formatIn,
		FormatOut:      formatOut,
		ChannelsIn:     channelsIn,
		ChannelsOut:    channelsOut,
		SampleRateIn:   sampleRateIn,
		SampleRateOut:  sampleRateOut,
		ChannelMixMode: ChannelMixModeDefault,
	}
	c.Resampling = ResamplerConfigInit(FormatF32, channelsIn, sampleRateIn, sampleRateOut, ResampleAlgorithmLinear)
	return c
}

// DataConverterConfigInitDefault mirrors ma_data_converter_config_init_default.
func DataConverterConfigInitDefault() DataConverterConfig {
	return DataConverterConfigInit(FormatUnknown, FormatUnknown, 0, 0, 0, 0)
}

// dataConverterExecutionPath mirrors ma_data_converter_execution_path.
type dataConverterExecutionPath uint32

const (
	executionPathPassthrough   dataConverterExecutionPath = iota // No conversion.
	executionPathFormatOnly                                      // Only format conversion.
	executionPathChannelsOnly                                    // Only channel conversion.
	executionPathResampleOnly                                    // Only resampling.
	executionPathResampleFirst                                   // All conversions, resampling as the first step.
	executionPathChannelsFirst                                   // All conversions, channel conversion as the first step.
)

// DataConverter mirrors ma_data_converter. It performs format conversion,
// channel conversion and resampling in an efficient order. All intermediate
// processing is f32; scratch buffers are reused so the audio path is
// allocation-free once warmed up.
type DataConverter struct {
	formatIn      Format
	formatOut     Format
	channelsIn    uint32
	channelsOut   uint32
	sampleRateIn  uint32
	sampleRateOut uint32
	ditherMode    DitherMode
	path          dataConverterExecutionPath

	channelConverter *ChannelConverter
	resampler        *Resampler

	// Scratch buffers for the pipeline, sized on demand.
	bufIn  []float32 // Input converted to f32 at input channel count.
	bufMid []float32 // After the first pipeline stage.
	bufOut []float32 // After the second pipeline stage.
}

// NewDataConverter mirrors ma_data_converter_init.
func NewDataConverter(config DataConverterConfig) (*DataConverter, error) {
	if config.FormatIn == FormatUnknown || config.FormatOut == FormatUnknown {
		return nil, ErrInvalidArgs
	}
	if config.ChannelsIn == 0 || config.ChannelsOut == 0 {
		return nil, ErrInvalidArgs
	}
	if config.SampleRateIn == 0 || config.SampleRateOut == 0 {
		return nil, ErrInvalidArgs
	}

	c := &DataConverter{
		formatIn:      config.FormatIn,
		formatOut:     config.FormatOut,
		channelsIn:    config.ChannelsIn,
		channelsOut:   config.ChannelsOut,
		sampleRateIn:  config.SampleRateIn,
		sampleRateOut: config.SampleRateOut,
		ditherMode:    config.DitherMode,
	}

	needsFormat := config.FormatIn != config.FormatOut
	needsChannels := config.ChannelsIn != config.ChannelsOut || !ChannelMapIsEqual(config.ChannelMapIn, config.ChannelMapOut, config.ChannelsIn)
	needsResample := config.SampleRateIn != config.SampleRateOut || config.AllowDynamicSampleRate

	if needsChannels {
		ccCfg := ChannelConverterConfigInit(FormatF32, config.ChannelsIn, config.ChannelMapIn, config.ChannelsOut, config.ChannelMapOut, config.ChannelMixMode)
		ccCfg.CalculateLFEFromSpatialChannels = config.CalculateLFEFromSpatialChannels
		ccCfg.Weights = config.ChannelWeights
		cc, err := NewChannelConverter(ccCfg)
		if err != nil {
			return nil, err
		}
		c.channelConverter = cc
	}

	if needsResample {
		// The resampler runs at whichever stage has fewer channels so the
		// per-frame cost is minimized.
		resampleChannels := min(config.ChannelsIn, config.ChannelsOut)
		if !needsChannels {
			resampleChannels = config.ChannelsIn
		}
		rsCfg := config.Resampling
		rsCfg.Format = FormatF32
		rsCfg.Channels = resampleChannels
		rsCfg.SampleRateIn = config.SampleRateIn
		rsCfg.SampleRateOut = config.SampleRateOut
		rs, err := NewResampler(rsCfg)
		if err != nil {
			return nil, err
		}
		c.resampler = rs
	}

	switch {
	case !needsFormat && !needsChannels && !needsResample:
		c.path = executionPathPassthrough
	case !needsChannels && !needsResample:
		c.path = executionPathFormatOnly
	case !needsResample:
		c.path = executionPathChannelsOnly
	case !needsChannels:
		c.path = executionPathResampleOnly
	case config.ChannelsIn > config.ChannelsOut:
		c.path = executionPathChannelsFirst // Reduce channels before resampling.
	default:
		c.path = executionPathResampleFirst // Resample at the lower channel count.
	}

	// Pre-allocate scratch buffers to cover typical device period sizes
	// (up to ~85 ms at 48 kHz). This eliminates GC pressure during audio
	// callback warmup; the buffers grow lazily if larger periods are used.
	const preAllocFrames = 4096
	if c.path != executionPathPassthrough && c.path != executionPathFormatOnly {
		c.bufIn = make([]float32, preAllocFrames*int(config.ChannelsIn))
		c.bufMid = make([]float32, preAllocFrames*int(config.ChannelsOut))
		c.bufOut = make([]float32, preAllocFrames*int(config.ChannelsOut))
	}

	return c, nil
}

func growF32(buf []float32, n int) []float32 {
	if cap(buf) < n {
		return make([]float32, n)
	}
	return buf[:n]
}

// Process mirrors ma_data_converter_process_pcm_frames. src may be nil to
// feed silence. Returns the number of input frames consumed and output
// frames written.
func (c *DataConverter) Process(dst, src []byte, frameCountIn, frameCountOut uint64) (framesIn, framesOut uint64, err error) {
	switch c.path {
	case executionPathPassthrough:
		n := min(frameCountIn, frameCountOut)
		bpf := FrameSizeInBytes(c.formatOut, c.channelsOut)
		if src == nil {
			SilencePCMFrames(dst, n, c.formatOut, c.channelsOut)
		} else {
			copy(dst[:int(n)*bpf], src[:int(n)*bpf])
		}
		return n, n, nil

	case executionPathFormatOnly:
		n := min(frameCountIn, frameCountOut)
		if src == nil {
			SilencePCMFrames(dst, n, c.formatOut, c.channelsOut)
		} else {
			if err := ConvertPCMFramesFormat(dst, c.formatOut, src, c.formatIn, n, c.channelsIn, c.ditherMode); err != nil {
				return 0, 0, err
			}
		}
		return n, n, nil
	}

	// All remaining paths run through f32 pipeline stages.
	cin := int(c.channelsIn)
	cout := int(c.channelsOut)

	// Stage 0: input -> f32.
	c.bufIn = growF32(c.bufIn, int(frameCountIn)*cin)
	if src == nil {
		for i := range c.bufIn {
			c.bufIn[i] = 0
		}
	} else {
		if err := PCMConvert(f32ToBytes(c.bufIn), FormatF32, src, c.formatIn, frameCountIn*uint64(cin), DitherModeNone); err != nil {
			return 0, 0, err
		}
	}

	var outF32 []float32
	var consumedIn, producedOut uint64

	switch c.path {
	case executionPathChannelsOnly:
		n := min(frameCountIn, frameCountOut)
		c.bufOut = growF32(c.bufOut, int(n)*cout)
		if err := c.channelConverter.ProcessF32(c.bufOut, c.bufIn, n); err != nil {
			return 0, 0, err
		}
		outF32 = c.bufOut[:int(n)*cout]
		consumedIn, producedOut = n, n

	case executionPathResampleOnly:
		c.bufOut = growF32(c.bufOut, int(frameCountOut)*cin)
		fin, fout, err := c.resampler.backendProcessF32(c.bufOut, c.bufIn, frameCountIn, frameCountOut)
		if err != nil {
			return 0, 0, err
		}
		outF32 = c.bufOut[:int(fout)*cin]
		consumedIn, producedOut = fin, fout

	case executionPathChannelsFirst:
		// Channels (in->out count), then resample at out count.
		c.bufMid = growF32(c.bufMid, int(frameCountIn)*cout)
		if err := c.channelConverter.ProcessF32(c.bufMid, c.bufIn, frameCountIn); err != nil {
			return 0, 0, err
		}
		c.bufOut = growF32(c.bufOut, int(frameCountOut)*cout)
		fin, fout, err := c.resampler.backendProcessF32(c.bufOut, c.bufMid, frameCountIn, frameCountOut)
		if err != nil {
			return 0, 0, err
		}
		outF32 = c.bufOut[:int(fout)*cout]
		consumedIn, producedOut = fin, fout

	case executionPathResampleFirst:
		// Resample at in count, then channels (in->out count).
		c.bufMid = growF32(c.bufMid, int(frameCountOut)*cin)
		fin, fout, err := c.resampler.backendProcessF32(c.bufMid, c.bufIn, frameCountIn, frameCountOut)
		if err != nil {
			return 0, 0, err
		}
		c.bufOut = growF32(c.bufOut, int(fout)*cout)
		if err := c.channelConverter.ProcessF32(c.bufOut, c.bufMid, fout); err != nil {
			return 0, 0, err
		}
		outF32 = c.bufOut[:int(fout)*cout]
		consumedIn, producedOut = fin, fout
	}

	// Final stage: f32 -> output format.
	if err := PCMConvert(dst, c.formatOut, f32ToBytes(outF32), FormatF32, producedOut*uint64(cout), c.ditherMode); err != nil {
		return 0, 0, err
	}
	return consumedIn, producedOut, nil
}

// backendProcessF32 lets the data converter drive the resampler directly on
// f32 buffers, bypassing byte-slice conversion.
func (r *Resampler) backendProcessF32(dst, src []float32, frameCountIn, frameCountOut uint64) (uint64, uint64, error) {
	if lr, ok := r.backend.(*LinearResampler); ok {
		return lr.ProcessF32(dst, src, frameCountIn, frameCountOut)
	}
	return r.backend.Process(f32ToBytes(dst), f32ToBytes(src), frameCountIn, frameCountOut)
}

// SetRate mirrors ma_data_converter_set_rate. Requires
// AllowDynamicSampleRate or matching construction rates.
func (c *DataConverter) SetRate(sampleRateIn, sampleRateOut uint32) error {
	if c.resampler == nil {
		if sampleRateIn == c.sampleRateIn && sampleRateOut == c.sampleRateOut {
			return nil
		}
		return ErrInvalidOperation
	}
	if err := c.resampler.SetRate(sampleRateIn, sampleRateOut); err != nil {
		return err
	}
	c.sampleRateIn = sampleRateIn
	c.sampleRateOut = sampleRateOut
	return nil
}

// SetRateRatio mirrors ma_data_converter_set_rate_ratio.
func (c *DataConverter) SetRateRatio(ratio float32) error {
	if c.resampler == nil {
		return ErrInvalidOperation
	}
	if err := c.resampler.SetRateRatio(ratio); err != nil {
		return err
	}
	c.sampleRateIn = c.resampler.SampleRateIn()
	c.sampleRateOut = c.resampler.SampleRateOut()
	return nil
}

// RequiredInputFrameCount mirrors ma_data_converter_get_required_input_frame_count.
func (c *DataConverter) RequiredInputFrameCount(outputFrameCount uint64) uint64 {
	if c.resampler == nil {
		return outputFrameCount
	}
	return c.resampler.RequiredInputFrameCount(outputFrameCount)
}

// ExpectedOutputFrameCount mirrors ma_data_converter_get_expected_output_frame_count.
func (c *DataConverter) ExpectedOutputFrameCount(inputFrameCount uint64) uint64 {
	if c.resampler == nil {
		return inputFrameCount
	}
	return c.resampler.ExpectedOutputFrameCount(inputFrameCount)
}

// InputLatencyInFrames mirrors ma_data_converter_get_input_latency.
func (c *DataConverter) InputLatencyInFrames() uint64 {
	if c.resampler == nil {
		return 0
	}
	return c.resampler.InputLatencyInFrames()
}

// OutputLatencyInFrames mirrors ma_data_converter_get_output_latency.
func (c *DataConverter) OutputLatencyInFrames() uint64 {
	if c.resampler == nil {
		return 0
	}
	return c.resampler.OutputLatencyInFrames()
}

// Reset mirrors ma_data_converter_reset.
func (c *DataConverter) Reset() {
	if c.resampler != nil {
		c.resampler.Reset()
	}
}

// InputFormat returns the configured input format.
func (c *DataConverter) InputFormat() Format { return c.formatIn }

// OutputFormat returns the configured output format.
func (c *DataConverter) OutputFormat() Format { return c.formatOut }

// InputChannels returns the configured input channel count.
func (c *DataConverter) InputChannels() uint32 { return c.channelsIn }

// OutputChannels returns the configured output channel count.
func (c *DataConverter) OutputChannels() uint32 { return c.channelsOut }

// SampleRateIn returns the current input sample rate.
func (c *DataConverter) SampleRateIn() uint32 { return c.sampleRateIn }

// SampleRateOut returns the current output sample rate.
func (c *DataConverter) SampleRateOut() uint32 { return c.sampleRateOut }
