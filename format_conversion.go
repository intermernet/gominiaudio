package gominiaudio

import (
	"encoding/binary"
	"math"
)

// lcg mirrors ma_lcg, the PRNG used for dithering.
type lcg struct {
	state int32
}

const (
	lcgM = 2147483647
	lcgA = 48271
	lcgC = 0
)

func newLCG() lcg { return lcg{state: 4321} }

func (l *lcg) randS32() int32 {
	l.state = int32((int64(lcgA)*int64(l.state) + lcgC) % lcgM)
	return l.state
}

func (l *lcg) randF64() float64 {
	return float64(l.randS32()) / float64(0x7FFFFFFF) // [0, 1]
}

func (l *lcg) randRangeS32(lo, hi int32) int32 {
	if lo == hi {
		return lo
	}
	return lo + l.randS32()/(0x7FFFFFFF/(hi-lo+1))
}

func (l *lcg) randRangeF64(lo, hi float64) float64 {
	return lo + l.randF64()*(hi-lo)
}

// ditherF64 mirrors ma_dither_f32/f64.
func (l *lcg) ditherF64(mode DitherMode, ditherMin, ditherMax float64) float64 {
	switch mode {
	case DitherModeRectangle:
		return l.randRangeF64(ditherMin, ditherMax)
	case DitherModeTriangle:
		a := l.randRangeF64(ditherMin, 0)
		b := l.randRangeF64(0, ditherMax)
		return a + b
	default:
		return 0
	}
}

// ditherS32 mirrors ma_dither_s32.
func (l *lcg) ditherS32(mode DitherMode, ditherMin, ditherMax int32) int32 {
	switch mode {
	case DitherModeRectangle:
		return l.randRangeS32(ditherMin, ditherMax)
	case DitherModeTriangle:
		a := l.randRangeS32(ditherMin, 0)
		b := l.randRangeS32(0, ditherMax)
		return a + b
	default:
		return 0
	}
}

// global dither RNG. miniaudio uses a global LCG seeded once; contention is
// not a concern because dithering tolerates interleaved sequences.
var globalLCG = newLCG()

func clampS32(x int64) int32 {
	if x < math.MinInt32 {
		return math.MinInt32
	}
	if x > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(x)
}

// readS24 reads a tightly packed little-endian signed 24-bit sample.
func readS24(b []byte) int32 {
	u := uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
	return int32(u<<8) >> 8
}

// writeS24 writes a tightly packed little-endian signed 24-bit sample.
func writeS24(b []byte, v int32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
}

// PCMConvert mirrors ma_pcm_convert: converts sampleCount samples from
// formatIn to formatOut. dst and src must hold at least sampleCount samples
// of their respective formats. Conversions between integer formats are pure
// bit shifts (identical to miniaudio); conversions involving f32 use
// miniaudio's scaling. Dithering is applied when reducing bit depth.
func PCMConvert(dst []byte, formatOut Format, src []byte, formatIn Format, sampleCount uint64, ditherMode DitherMode) error {
	if formatOut == FormatUnknown || formatIn == FormatUnknown {
		return ErrInvalidArgs
	}
	if formatOut == formatIn {
		copy(dst[:sampleCount*uint64(formatIn.SizeInBytes())], src)
		return nil
	}

	n := int(sampleCount)
	inSize := formatIn.SizeInBytes()
	outSize := formatOut.SizeInBytes()
	if len(src) < n*inSize || len(dst) < n*outSize {
		return ErrInvalidArgs
	}

	// Special-case u8<->f32 to match miniaudio's asymmetric u8 scaling.
	if formatIn == FormatU8 && formatOut == FormatF32 {
		for i := 0; i < n; i++ {
			x := float64(src[i])
			x = x * 0.00784313725490196078 // 2/255
			x = x - 1
			binary.LittleEndian.PutUint32(dst[i*4:], math.Float32bits(float32(x)))
		}
		return nil
	}
	if formatIn == FormatF32 && formatOut == FormatU8 {
		ditherMin, ditherMax := 0.0, 0.0
		if ditherMode != DitherModeNone {
			ditherMin = -(1.0 / 255)
			ditherMax = 1.0 / 255
		}
		for i := 0; i < n; i++ {
			x := float64(math.Float32frombits(binary.LittleEndian.Uint32(src[i*4:])))
			x += globalLCG.ditherF64(ditherMode, ditherMin, ditherMax)
			x = clampF64(x)
			x = (x + 1) * 127.5
			dst[i] = byte(uint8(x))
		}
		return nil
	}

	if formatIn == FormatF32 {
		// f32 -> integer formats.
		// Specialized no-dither paths (s16 dispatches to SIMD when built).
		if ditherMode == DitherModeNone {
			switch formatOut {
			case FormatS16:
				convertF32ToS16Impl(dst, bytesToF32(src), n)
				return nil
			case FormatS24:
				convertF32ToS24Scalar(dst, bytesToF32(src), n)
				return nil
			case FormatS32:
				convertF32ToS32Scalar(dst, bytesToF32(src), n)
				return nil
			}
		}
		var scale float64
		switch formatOut {
		case FormatS16:
			scale = 32767.0
		case FormatS24:
			scale = 8388607.0
		case FormatS32:
			scale = 2147483647.0
		}
		ditherMin, ditherMax := 0.0, 0.0
		if ditherMode != DitherModeNone {
			ditherMin = -(1.0 / (scale + 1))
			ditherMax = 1.0 / (scale + 1)
		}
		for i := 0; i < n; i++ {
			x := float64(math.Float32frombits(binary.LittleEndian.Uint32(src[i*4:])))
			x += globalLCG.ditherF64(ditherMode, ditherMin, ditherMax)
			x = clampF64(x)
			v := int32(x * scale)
			switch formatOut {
			case FormatS16:
				binary.LittleEndian.PutUint16(dst[i*2:], uint16(int16(v)))
			case FormatS24:
				writeS24(dst[i*3:], v)
			case FormatS32:
				binary.LittleEndian.PutUint32(dst[i*4:], uint32(v))
			}
		}
		return nil
	}

	if formatOut == FormatF32 {
		// integer formats -> f32: specialized per-format loops (s16
		// dispatches to SIMD when built).
		switch formatIn {
		case FormatS16:
			convertS16ToF32Impl(bytesToF32(dst), src, n)
		case FormatS24:
			convertS24ToF32Scalar(bytesToF32(dst), src, n)
		case FormatS32:
			convertS32ToF32Scalar(bytesToF32(dst), src, n)
		}
		return nil
	}

	// Integer <-> integer: use the s32 domain as a hub. Up-conversions are
	// left shifts, down-conversions are dithered right shifts. This is
	// arithmetically identical to miniaudio's direct pairwise conversions.
	srcShift := integerShift(formatIn)
	dstShift := integerShift(formatOut)

	for i := 0; i < n; i++ {
		var v int32
		switch formatIn {
		case FormatU8:
			v = (int32(src[i]) - 128) << srcShift
		case FormatS16:
			v = int32(int16(binary.LittleEndian.Uint16(src[i*2:]))) << srcShift
		case FormatS24:
			v = readS24(src[i*3:]) << srcShift
		case FormatS32:
			v = int32(binary.LittleEndian.Uint32(src[i*4:]))
		}

		if dstShift > srcShift && ditherMode != DitherModeNone {
			// Reducing bit depth: dither at half an LSB of the target,
			// expressed in the s32 hub domain, then clamp.
			lsb := int32(1) << dstShift
			d := globalLCG.ditherS32(ditherMode, -(lsb >> 1), (lsb>>1)-1)
			v = clampS32(int64(v) + int64(d))
		}

		switch formatOut {
		case FormatU8:
			dst[i] = byte(int32(v>>dstShift) + 128)
		case FormatS16:
			binary.LittleEndian.PutUint16(dst[i*2:], uint16(int16(v>>dstShift)))
		case FormatS24:
			writeS24(dst[i*3:], v>>dstShift)
		case FormatS32:
			binary.LittleEndian.PutUint32(dst[i*4:], uint32(v))
		}
	}
	return nil
}

// integerShift returns the left-shift needed to bring a sample of format f
// into the s32 domain.
func integerShift(f Format) uint {
	switch f {
	case FormatU8:
		return 24
	case FormatS16:
		return 16
	case FormatS24:
		return 8
	default:
		return 0
	}
}

func clampF64(x float64) float64 {
	if x < -1 {
		return -1
	}
	if x > 1 {
		return 1
	}
	return x
}

// ConvertPCMFramesFormat mirrors ma_convert_pcm_frames_format.
func ConvertPCMFramesFormat(dst []byte, formatOut Format, src []byte, formatIn Format, frameCount uint64, channels uint32, ditherMode DitherMode) error {
	return PCMConvert(dst, formatOut, src, formatIn, frameCount*uint64(channels), ditherMode)
}

// DeinterleavePCMFrames mirrors ma_deinterleave_pcm_frames. src holds
// interleaved frames; dst is one destination slice per channel.
func DeinterleavePCMFrames(format Format, channels uint32, frameCount uint64, src []byte, dst [][]byte) error {
	if format == FormatUnknown || len(dst) < int(channels) {
		return ErrInvalidArgs
	}
	ss := format.SizeInBytes()
	for f := uint64(0); f < frameCount; f++ {
		for ch := uint32(0); ch < channels; ch++ {
			srcOff := int(f*uint64(channels)+uint64(ch)) * ss
			dstOff := int(f) * ss
			copy(dst[ch][dstOff:dstOff+ss], src[srcOff:srcOff+ss])
		}
	}
	return nil
}

// convertF32ToS16Impl is selected at init time; defaults to scalar.
var convertF32ToS16Impl func(dst []byte, src []float32, n int) = convertF32ToS16Scalar

// convertS16ToF32Impl is selected at init time; defaults to scalar.
var convertS16ToF32Impl func(dst []float32, src []byte, n int) = convertS16ToF32Scalar

const s16Scale = float32(32767.0)

// convertF32ToS16Scalar converts n float32 samples (clamped [-1,1]) to s16.
func convertF32ToS16Scalar(dst []byte, src []float32, n int) {
	for i := 0; i < n; i++ {
		x := src[i]
		if x > 1 {
			x = 1
		} else if x < -1 {
			x = -1
		}
		v := int16(x * s16Scale)
		dst[i*2] = byte(v)
		dst[i*2+1] = byte(uint16(v) >> 8)
	}
}

// convertS16ToF32Scalar converts n s16 samples to float32 in [-1,1].
func convertS16ToF32Scalar(dst []float32, src []byte, n int) {
	for i := 0; i < n; i++ {
		v := int16(uint16(src[i*2]) | uint16(src[i*2+1])<<8)
		dst[i] = float32(v) / 32768.0
	}
}

// convertF32ToS24Scalar converts n float32 samples (clamped [-1,1]) to
// tightly packed little-endian s24. Relevant on the device hot path:
// exclusive-mode WASAPI commonly negotiates 24-bit hardware formats.
func convertF32ToS24Scalar(dst []byte, src []float32, n int) {
	for i := 0; i < n; i++ {
		x := src[i]
		if x > 1 {
			x = 1
		} else if x < -1 {
			x = -1
		}
		v := int32(x * 8388607.0)
		dst[i*3] = byte(v)
		dst[i*3+1] = byte(v >> 8)
		dst[i*3+2] = byte(v >> 16)
	}
}

// convertS24ToF32Scalar converts n tightly packed little-endian s24 samples
// to float32 in [-1,1].
func convertS24ToF32Scalar(dst []float32, src []byte, n int) {
	for i := 0; i < n; i++ {
		u := uint32(src[i*3]) | uint32(src[i*3+1])<<8 | uint32(src[i*3+2])<<16
		v := int32(u<<8) >> 8
		dst[i] = float32(v) / 8388608.0
	}
}

// convertF32ToS32Scalar converts n float32 samples (clamped [-1,1]) to s32.
// The scale multiply stays in float64: float32(2147483647) rounds up to
// 2^31, whose int32 conversion would overflow at full scale.
func convertF32ToS32Scalar(dst []byte, src []float32, n int) {
	for i := 0; i < n; i++ {
		x := float64(src[i])
		if x > 1 {
			x = 1
		} else if x < -1 {
			x = -1
		}
		v := int32(x * 2147483647.0)
		binary.LittleEndian.PutUint32(dst[i*4:], uint32(v))
	}
}

// convertS32ToF32Scalar converts n s32 samples to float32 in [-1,1].
func convertS32ToF32Scalar(dst []float32, src []byte, n int) {
	const inv = 1.0 / 2147483648.0
	for i := 0; i < n; i++ {
		v := int32(binary.LittleEndian.Uint32(src[i*4:]))
		dst[i] = float32(float64(v) * inv)
	}
}

// InterleavePCMFrames mirrors ma_interleave_pcm_frames. src is one source
// slice per channel; dst receives interleaved frames.
func InterleavePCMFrames(format Format, channels uint32, frameCount uint64, src [][]byte, dst []byte) error {
	if format == FormatUnknown || len(src) < int(channels) {
		return ErrInvalidArgs
	}
	ss := format.SizeInBytes()
	for f := uint64(0); f < frameCount; f++ {
		for ch := uint32(0); ch < channels; ch++ {
			srcOff := int(f) * ss
			dstOff := int(f*uint64(channels)+uint64(ch)) * ss
			copy(dst[dstOff:dstOff+ss], src[ch][srcOff:srcOff+ss])
		}
	}
	return nil
}
