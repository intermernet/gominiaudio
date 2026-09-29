package gominiaudio

import "math"

// VolumeLinearToDB mirrors ma_volume_linear_to_db.
func VolumeLinearToDB(factor float32) float32 {
	return float32(20 * math.Log10(float64(factor)))
}

// VolumeDBToLinear mirrors ma_volume_db_to_linear.
func VolumeDBToLinear(gain float32) float32 {
	return float32(math.Pow(10, float64(gain)/20))
}

// clipSamplesF32Impl is set to a SIMD implementation on capable hardware
// (see volume_simd.go). Otherwise it falls back to the scalar version.
var clipSamplesF32Impl func(dst, src []float32, count uint64) = clipSamplesF32Scalar

// copyApplyVolumeF32Impl is the inner implementation selected at init time.
var copyApplyVolumeF32Impl func(dst, src []float32, count uint64, factor float32) = copyApplyVolumeF32Scalar

// copyApplyVolumeAndClipF32Impl combines volume scale + clamp in one pass.
var copyApplyVolumeAndClipF32Impl func(dst, src []float32, count uint64, volume float32) = copyApplyVolumeAndClipF32Scalar

// mixPCMFramesF32Impl is the inner implementation selected at init time.
var mixPCMFramesF32Impl func(dst, src []float32, sampleCount int, volume float32) = mixPCMFramesF32Scalar

// clipSamplesF32Scalar is the portable fallback for ClipSamplesF32.
func clipSamplesF32Scalar(dst, src []float32, count uint64) {
	for i := uint64(0); i < count; i++ {
		x := src[i]
		if x < -1 {
			x = -1
		} else if x > 1 {
			x = 1
		}
		dst[i] = x
	}
}

// copyApplyVolumeF32Scalar is the portable fallback.
func copyApplyVolumeF32Scalar(dst, src []float32, count uint64, factor float32) {
	for i := uint64(0); i < count; i++ {
		dst[i] = src[i] * factor
	}
}

// copyApplyVolumeAndClipF32Scalar is the portable fallback.
func copyApplyVolumeAndClipF32Scalar(dst, src []float32, count uint64, volume float32) {
	for i := uint64(0); i < count; i++ {
		x := src[i] * volume
		if x < -1 {
			x = -1
		} else if x > 1 {
			x = 1
		}
		dst[i] = x
	}
}

// mixPCMFramesF32Scalar is the portable fallback for MixPCMFramesF32.
func mixPCMFramesF32Scalar(dst, src []float32, sampleCount int, volume float32) {
	if volume == 1 {
		for i := 0; i < sampleCount; i++ {
			dst[i] += src[i]
		}
	} else {
		for i := 0; i < sampleCount; i++ {
			dst[i] += src[i] * volume
		}
	}
}

// ClipSamplesF32 mirrors ma_clip_samples_f32: clamps samples to [-1, 1].
func ClipSamplesF32(dst, src []float32, count uint64) {
	clipSamplesF32Impl(dst, src, count)
}

// ClipPCMFrames mirrors ma_clip_pcm_frames. For integer formats this is a
// no-op copy since values are inherently clipped; for f32 it clamps.
func ClipPCMFrames(dst, src []byte, frameCount uint64, format Format, channels uint32) {
	if format == FormatF32 {
		ClipSamplesF32(bytesToF32(dst), bytesToF32(src), frameCount*uint64(channels))
		return
	}
	n := int(frameCount) * FrameSizeInBytes(format, channels)
	if &dst[0] != &src[0] {
		copy(dst[:n], src[:n])
	}
}

// CopyAndApplyVolumeFactorF32 mirrors ma_copy_and_apply_volume_factor_f32.
// dst and src may be the same slice.
func CopyAndApplyVolumeFactorF32(dst, src []float32, count uint64, factor float32) {
	copyApplyVolumeF32Impl(dst, src, count, factor)
}

// ApplyVolumeFactorF32 mirrors ma_apply_volume_factor_f32 (in-place).
func ApplyVolumeFactorF32(samples []float32, count uint64, factor float32) {
	CopyAndApplyVolumeFactorF32(samples, samples, count, factor)
}

// CopyAndApplyVolumeFactorS16 mirrors ma_copy_and_apply_volume_factor_s16.
func CopyAndApplyVolumeFactorS16(dst, src []int16, count uint64, factor float32) {
	for i := uint64(0); i < count; i++ {
		dst[i] = int16(clampF64(float64(src[i])*float64(factor)/32768.0) * 32767.0)
	}
}

// ApplyVolumeFactorS16 mirrors ma_apply_volume_factor_s16 (in-place).
func ApplyVolumeFactorS16(samples []int16, count uint64, factor float32) {
	CopyAndApplyVolumeFactorS16(samples, samples, count, factor)
}

// CopyAndApplyVolumeFactorPCMFrames mirrors
// ma_copy_and_apply_volume_factor_pcm_frames for all formats. dst and src may
// alias.
func CopyAndApplyVolumeFactorPCMFrames(dst, src []byte, frameCount uint64, format Format, channels uint32, factor float32) error {
	sampleCount := frameCount * uint64(channels)
	switch format {
	case FormatF32:
		CopyAndApplyVolumeFactorF32(bytesToF32(dst), bytesToF32(src), sampleCount, factor)
		return nil
	case FormatS16:
		CopyAndApplyVolumeFactorS16(bytesToS16(dst), bytesToS16(src), sampleCount, factor)
		return nil
	case FormatU8:
		for i := uint64(0); i < sampleCount; i++ {
			x := (float64(src[i]) - 128) / 128
			x = clampF64(x * float64(factor))
			dst[i] = byte(int32(x*127) + 128)
		}
		return nil
	case FormatS24:
		for i := uint64(0); i < sampleCount; i++ {
			v := readS24(src[i*3:])
			x := clampF64(float64(v) / 8388608 * float64(factor))
			writeS24(dst[i*3:], int32(x*8388607))
		}
		return nil
	case FormatS32:
		for i := uint64(0); i < sampleCount; i++ {
			v := int32(uint32(src[i*4]) | uint32(src[i*4+1])<<8 | uint32(src[i*4+2])<<16 | uint32(src[i*4+3])<<24)
			x := clampF64(float64(v) / 2147483648 * float64(factor))
			nv := int32(x * 2147483647)
			dst[i*4] = byte(nv)
			dst[i*4+1] = byte(nv >> 8)
			dst[i*4+2] = byte(nv >> 16)
			dst[i*4+3] = byte(nv >> 24)
		}
		return nil
	default:
		return ErrInvalidArgs
	}
}

// ApplyVolumeFactorPCMFrames mirrors ma_apply_volume_factor_pcm_frames.
func ApplyVolumeFactorPCMFrames(frames []byte, frameCount uint64, format Format, channels uint32, factor float32) error {
	return CopyAndApplyVolumeFactorPCMFrames(frames, frames, frameCount, format, channels, factor)
}

// CopyAndApplyVolumeFactorPerChannelF32 mirrors
// ma_copy_and_apply_volume_factor_per_channel_f32.
func CopyAndApplyVolumeFactorPerChannelF32(dst, src []float32, frameCount uint64, channels uint32, channelGains []float32) error {
	if len(channelGains) < int(channels) {
		return ErrInvalidArgs
	}
	ch := int(channels)
	for f := 0; f < int(frameCount); f++ {
		for c := 0; c < ch; c++ {
			dst[f*ch+c] = src[f*ch+c] * channelGains[c]
		}
	}
	return nil
}

// CopyAndApplyVolumeAndClipSamplesF32 mirrors
// ma_copy_and_apply_volume_and_clip_samples_f32.
func CopyAndApplyVolumeAndClipSamplesF32(dst, src []float32, count uint64, volume float32) {
	copyApplyVolumeAndClipF32Impl(dst, src, count, volume)
}

// MixPCMFramesF32 mirrors ma_mix_pcm_frames_f32: dst += src * volume.
func MixPCMFramesF32(dst, src []float32, frameCount uint64, channels uint32, volume float32) error {
	sampleCount := int(frameCount) * int(channels)
	if len(dst) < sampleCount || len(src) < sampleCount {
		return ErrInvalidArgs
	}
	if volume == 0 {
		return nil
	}
	mixPCMFramesF32Impl(dst, src, sampleCount, volume)
	return nil
}
