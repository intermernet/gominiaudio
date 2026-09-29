package gominiaudio

// ChannelMixMode mirrors ma_channel_mix_mode.
type ChannelMixMode uint32

const (
	ChannelMixModeRectangular   ChannelMixMode = 0 // Simple averaging based on the plane(s) the channel is sitting on.
	ChannelMixModeSimple        ChannelMixMode = 1 // Drop excess channels; zeroed out extra channels.
	ChannelMixModeCustomWeights ChannelMixMode = 2 // Use custom weights specified in ChannelConverterConfig.
	ChannelMixModeDefault                      = ChannelMixModeRectangular
)

// channelConversionPath mirrors ma_channel_conversion_path.
type channelConversionPath uint32

const (
	channelConversionPathUnknown channelConversionPath = iota
	channelConversionPathPassthrough
	channelConversionPathMonoOut // Converting to mono.
	channelConversionPathMonoIn  // Converting from mono.
	channelConversionPathShuffle // Simple rearrangement. Includes 1:1 mapping with a different order.
	channelConversionPathWeights // Blended based on weights.
)

// channelPlaneRatios mirrors g_maChannelPlaneRatios: for each channel
// position, the proportion of its energy on each of the 6 planes
// (left, right, front, back, ceiling, floor).
var channelPlaneRatios = map[Channel][6]float32{
	ChannelNone:             {0, 0, 0, 0, 0, 0},
	ChannelMono:             {0, 0, 0, 0, 0, 0},
	ChannelFrontLeft:        {0.5, 0, 0.5, 0, 0, 0},
	ChannelFrontRight:       {0, 0.5, 0.5, 0, 0, 0},
	ChannelFrontCenter:      {0, 0, 1.0, 0, 0, 0},
	ChannelLFE:              {0, 0, 0, 0, 0, 0},
	ChannelBackLeft:         {0.5, 0, 0, 0.5, 0, 0},
	ChannelBackRight:        {0, 0.5, 0, 0.5, 0, 0},
	ChannelFrontLeftCenter:  {0.25, 0, 0.75, 0, 0, 0},
	ChannelFrontRightCenter: {0, 0.25, 0.75, 0, 0, 0},
	ChannelBackCenter:       {0, 0, 0, 1.0, 0, 0},
	ChannelSideLeft:         {1.0, 0, 0, 0, 0, 0},
	ChannelSideRight:        {0, 1.0, 0, 0, 0, 0},
	ChannelTopCenter:        {0, 0, 0, 0, 1.0, 0},
	ChannelTopFrontLeft:     {0.33, 0, 0.33, 0, 0.34, 0},
	ChannelTopFrontCenter:   {0, 0, 0.5, 0, 0.5, 0},
	ChannelTopFrontRight:    {0, 0.33, 0.33, 0, 0.34, 0},
	ChannelTopBackLeft:      {0.33, 0, 0, 0.33, 0.34, 0},
	ChannelTopBackCenter:    {0, 0, 0, 0.5, 0.5, 0},
	ChannelTopBackRight:     {0, 0.33, 0, 0.33, 0.34, 0},
}

// calculateChannelPositionRectangularWeight mirrors
// ma_calculate_channel_position_rectangular_weight.
func calculateChannelPositionRectangularWeight(a, b Channel) float32 {
	ra, okA := channelPlaneRatios[a]
	rb, okB := channelPlaneRatios[b]
	if !okA || !okB {
		return 0
	}
	var contribution float32
	for p := 0; p < 6; p++ {
		contribution += ra[p] * rb[p]
	}
	return contribution
}

// ChannelConverterConfig mirrors ma_channel_converter_config.
type ChannelConverterConfig struct {
	Format                          Format
	ChannelsIn                      uint32
	ChannelsOut                     uint32
	ChannelMapIn                    []Channel // nil implies the default standard layout.
	ChannelMapOut                   []Channel // nil implies the default standard layout.
	MixingMode                      ChannelMixMode
	CalculateLFEFromSpatialChannels bool        // When an output LFE is present, but no input LFE, generate the LFE from spatial channels.
	Weights                         [][]float32 // [ChannelsIn][ChannelsOut]. Only used when MixingMode is ChannelMixModeCustomWeights.
}

// ChannelConverterConfigInit mirrors ma_channel_converter_config_init.
func ChannelConverterConfigInit(format Format, channelsIn uint32, channelMapIn []Channel, channelsOut uint32, channelMapOut []Channel, mixingMode ChannelMixMode) ChannelConverterConfig {
	return ChannelConverterConfig{
		Format:        format,
		ChannelsIn:    channelsIn,
		ChannelsOut:   channelsOut,
		ChannelMapIn:  channelMapIn,
		ChannelMapOut: channelMapOut,
		MixingMode:    mixingMode,
	}
}

// ChannelConverter mirrors ma_channel_converter. It converts between channel
// counts and channel maps. Sample rate and format are left unchanged.
type ChannelConverter struct {
	format        Format
	channelsIn    uint32
	channelsOut   uint32
	channelMapIn  []Channel
	channelMapOut []Channel
	mixingMode    ChannelMixMode
	path          channelConversionPath
	shuffleTable  []int       // [channelsOut] -> source channel index, -1 for silence.
	weights       [][]float32 // [channelsIn][channelsOut]
	scratchIn     []float32   // Reused for non-f32 processing to avoid allocations on the audio path.
	scratchOut    []float32
}

// NewChannelConverter mirrors ma_channel_converter_init.
func NewChannelConverter(config ChannelConverterConfig) (*ChannelConverter, error) {
	if config.ChannelsIn == 0 || config.ChannelsOut == 0 {
		return nil, ErrInvalidArgs
	}
	if config.ChannelsIn > MaxChannels || config.ChannelsOut > MaxChannels {
		return nil, ErrInvalidArgs
	}
	if !ChannelMapIsValid(config.ChannelMapIn, config.ChannelsIn) || !ChannelMapIsValid(config.ChannelMapOut, config.ChannelsOut) {
		return nil, ErrInvalidArgs
	}

	c := &ChannelConverter{
		format:      config.Format,
		channelsIn:  config.ChannelsIn,
		channelsOut: config.ChannelsOut,
		mixingMode:  config.MixingMode,
	}

	c.channelMapIn = make([]Channel, config.ChannelsIn)
	for i := uint32(0); i < config.ChannelsIn; i++ {
		c.channelMapIn[i] = ChannelMapGetChannel(config.ChannelMapIn, config.ChannelsIn, i)
	}
	c.channelMapOut = make([]Channel, config.ChannelsOut)
	for i := uint32(0); i < config.ChannelsOut; i++ {
		c.channelMapOut[i] = ChannelMapGetChannel(config.ChannelMapOut, config.ChannelsOut, i)
	}

	// Determine the conversion path, mirroring miniaudio's logic.
	switch {
	case c.channelsIn == c.channelsOut && ChannelMapIsEqual(c.channelMapIn, c.channelMapOut, c.channelsIn):
		c.path = channelConversionPathPassthrough
	case c.channelsOut == 1 && (c.channelMapOut[0] == ChannelMono || ChannelMapIsBlank(c.channelMapOut, 1)):
		c.path = channelConversionPathMonoOut
	case c.channelsIn == 1 && (c.channelMapIn[0] == ChannelMono || ChannelMapIsBlank(c.channelMapIn, 1)):
		c.path = channelConversionPathMonoIn
	case c.mixingMode == ChannelMixModeSimple:
		c.path = channelConversionPathShuffle
	case c.channelsIn == c.channelsOut && isShufflable(c.channelMapIn, c.channelMapOut):
		c.path = channelConversionPathShuffle
	default:
		c.path = channelConversionPathWeights
	}

	if c.path == channelConversionPathShuffle {
		c.shuffleTable = make([]int, c.channelsOut)
		for o := uint32(0); o < c.channelsOut; o++ {
			c.shuffleTable[o] = ChannelMapFindChannelPosition(c.channelsIn, c.channelMapIn, c.channelMapOut[o])
		}
	}

	if c.path == channelConversionPathWeights {
		c.weights = make([][]float32, c.channelsIn)
		for i := range c.weights {
			c.weights[i] = make([]float32, c.channelsOut)
		}
		if config.MixingMode == ChannelMixModeCustomWeights {
			if config.Weights == nil {
				return nil, ErrInvalidArgs
			}
			for i := uint32(0); i < c.channelsIn; i++ {
				for o := uint32(0); o < c.channelsOut; o++ {
					c.weights[i][o] = config.Weights[i][o]
				}
			}
		} else {
			c.buildRectangularWeights(config.CalculateLFEFromSpatialChannels)
		}
	}

	return c, nil
}

func isShufflable(mapIn, mapOut []Channel) bool {
	// Shufflable when every output position exists exactly once in the input.
	n := uint32(len(mapOut))
	for _, out := range mapOut {
		count := 0
		for _, in := range mapIn {
			if in == out {
				count++
			}
		}
		if count != 1 {
			return false
		}
	}
	_ = n
	return true
}

func (c *ChannelConverter) buildRectangularWeights(calculateLFEFromSpatialChannels bool) {
	// Directly matching positions get a weight of 1. Otherwise use the
	// rectangular plane intersection weight, mirroring miniaudio.
	for i := uint32(0); i < c.channelsIn; i++ {
		for o := uint32(0); o < c.channelsOut; o++ {
			in := c.channelMapIn[i]
			out := c.channelMapOut[o]
			if in == out {
				c.weights[i][o] = 1
			} else if isSpatialChannelPosition(in) && isSpatialChannelPosition(out) {
				c.weights[i][o] = calculateChannelPositionRectangularWeight(in, out)
			}
		}
	}

	// Optionally derive the output LFE from spatial input channels when the
	// input has no LFE of its own.
	if calculateLFEFromSpatialChannels &&
		!ChannelMapContainsChannelPosition(c.channelsIn, c.channelMapIn, ChannelLFE) {
		for o := uint32(0); o < c.channelsOut; o++ {
			if c.channelMapOut[o] != ChannelLFE {
				continue
			}
			spatialCount := 0
			for i := uint32(0); i < c.channelsIn; i++ {
				if isSpatialChannelPosition(c.channelMapIn[i]) {
					spatialCount++
				}
			}
			if spatialCount == 0 {
				continue
			}
			for i := uint32(0); i < c.channelsIn; i++ {
				if isSpatialChannelPosition(c.channelMapIn[i]) {
					c.weights[i][o] = 1.0 / float32(spatialCount)
				}
			}
		}
	}
}

func isSpatialChannelPosition(p Channel) bool {
	if p == ChannelNone || p == ChannelMono || p == ChannelLFE {
		return false
	}
	if p >= ChannelAux0 && p <= ChannelAux31 {
		return false
	}
	return true
}

// ProcessF32 converts interleaved f32 frames. dst must hold
// frameCount*ChannelsOut samples; src frameCount*ChannelsIn.
func (c *ChannelConverter) ProcessF32(dst, src []float32, frameCount uint64) error {
	cin := int(c.channelsIn)
	cout := int(c.channelsOut)
	n := int(frameCount)
	if len(src) < n*cin || len(dst) < n*cout {
		return ErrInvalidArgs
	}

	switch c.path {
	case channelConversionPathPassthrough:
		copy(dst[:n*cout], src[:n*cin])

	case channelConversionPathMonoOut:
		// Pre-check the full slice bounds once so per-iteration checks are
		// eliminated by the compiler.
		if n > 0 {
			_ = src[(n-1)*cin+cin-1]
			_ = dst[n-1]
		}
		for f := 0; f < n; f++ {
			var total float32
			for ch := 0; ch < cin; ch++ {
				total += src[f*cin+ch]
			}
			dst[f] = total / float32(cin)
		}

	case channelConversionPathMonoIn:
		for f := 0; f < n; f++ {
			s := src[f]
			for ch := 0; ch < cout; ch++ {
				if c.channelMapOut[ch] == ChannelLFE {
					dst[f*cout+ch] = 0
				} else {
					dst[f*cout+ch] = s
				}
			}
		}

	case channelConversionPathShuffle:
		for f := 0; f < n; f++ {
			for ch := 0; ch < cout; ch++ {
				si := c.shuffleTable[ch]
				if si < 0 {
					dst[f*cout+ch] = 0
				} else {
					dst[f*cout+ch] = src[f*cin+si]
				}
			}
		}

	default: // weights
		channelConvertWeights(dst, src, c.weights, n, cin, cout)
	}
	return nil
}

// Process mirrors ma_channel_converter_process_pcm_frames, operating on
// interleaved frames in the converter's configured format.
func (c *ChannelConverter) Process(dst, src []byte, frameCount uint64) error {
	if c.format == FormatF32 {
		return c.ProcessF32(bytesToF32(dst), bytesToF32(src), frameCount)
	}

	// For non-f32 formats, convert to f32, process, convert back. The
	// passthrough and shuffle paths can work on raw bytes directly.
	ss := c.format.SizeInBytes()
	cin := int(c.channelsIn)
	cout := int(c.channelsOut)
	n := int(frameCount)

	switch c.path {
	case channelConversionPathPassthrough:
		copy(dst[:n*cout*ss], src[:n*cin*ss])
		return nil
	case channelConversionPathShuffle:
		for f := 0; f < n; f++ {
			for ch := 0; ch < cout; ch++ {
				si := c.shuffleTable[ch]
				dstOff := (f*cout + ch) * ss
				if si < 0 {
					writeSilence(dst[dstOff:dstOff+ss], c.format)
				} else {
					srcOff := (f*cin + si) * ss
					copy(dst[dstOff:dstOff+ss], src[srcOff:srcOff+ss])
				}
			}
		}
		return nil
	}

	if cap(c.scratchIn) < n*cin {
		c.scratchIn = make([]float32, n*cin)
	}
	if cap(c.scratchOut) < n*cout {
		c.scratchOut = make([]float32, n*cout)
	}
	srcF32 := c.scratchIn[:n*cin]
	dstF32 := c.scratchOut[:n*cout]
	if err := PCMConvert(f32ToBytes(srcF32), FormatF32, src, c.format, uint64(n*cin), DitherModeNone); err != nil {
		return err
	}
	if err := c.ProcessF32(dstF32, srcF32, frameCount); err != nil {
		return err
	}
	return PCMConvert(dst, c.format, f32ToBytes(dstF32), FormatF32, uint64(n*cout), DitherModeNone)
}

// InputChannels returns the number of input channels.
func (c *ChannelConverter) InputChannels() uint32 { return c.channelsIn }

// OutputChannels returns the number of output channels.
func (c *ChannelConverter) OutputChannels() uint32 { return c.channelsOut }

// InputChannelMap mirrors ma_channel_converter_get_input_channel_map.
func (c *ChannelConverter) InputChannelMap() []Channel {
	out := make([]Channel, len(c.channelMapIn))
	copy(out, c.channelMapIn)
	return out
}

// OutputChannelMap mirrors ma_channel_converter_get_output_channel_map.
func (c *ChannelConverter) OutputChannelMap() []Channel {
	out := make([]Channel, len(c.channelMapOut))
	copy(out, c.channelMapOut)
	return out
}

// writeSilence writes one sample of silence for the given format.
func writeSilence(dst []byte, format Format) {
	if format == FormatU8 {
		dst[0] = 128
		return
	}
	for i := range dst {
		dst[i] = 0
	}
}

// SilencePCMFrames mirrors ma_silence_pcm_frames.
func SilencePCMFrames(dst []byte, frameCount uint64, format Format, channels uint32) {
	sampleCount := int(frameCount) * int(channels)
	ss := format.SizeInBytes()
	if format == FormatU8 {
		for i := 0; i < sampleCount; i++ {
			dst[i] = 128
		}
		return
	}
	total := sampleCount * ss
	for i := 0; i < total && i < len(dst); i++ {
		dst[i] = 0
	}
}

// channelConvertWeightsImpl is the weight-mixing implementation; replaced at
// init time by a SIMD version on capable hardware (channel_converter_simd.go).
var channelConvertWeightsImpl = channelConvertWeightsScalar

// channelConvertWeights routes to the active implementation.
func channelConvertWeights(dst, src []float32, weights [][]float32, n, cin, cout int) {
	channelConvertWeightsImpl(dst, src, weights, n, cin, cout)
}

// channelConvertWeightsScalar is the portable weight-matrix mixer.
func channelConvertWeightsScalar(dst, src []float32, weights [][]float32, n, cin, cout int) {
	for f := 0; f < n; f++ {
		for o := 0; o < cout; o++ {
			var acc float32
			for i := 0; i < cin; i++ {
				acc += src[f*cin+i] * weights[i][o]
			}
			dst[f*cout+o] = acc
		}
	}
}
