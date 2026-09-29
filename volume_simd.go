//go:build goexperiment.simd

package gominiaudio

import "simd"

func init() {
	// simd.Emulated reports whether this hardware has no native vector
	// support, in which case the portable package falls back to a
	// per-element Go loop. Our own scalar code is at least as fast in that
	// case, so only switch over when there's real hardware behind it.
	if simd.Emulated() {
		return
	}
	clipSamplesF32Impl = clipSamplesF32SIMD
	copyApplyVolumeF32Impl = copyApplyVolumeF32SIMD
	copyApplyVolumeAndClipF32Impl = copyApplyVolumeAndClipF32SIMD
	mixPCMFramesF32Impl = mixPCMFramesF32SIMD
}

// clipSamplesF32SIMD clamps samples to [-1, 1] using the portable simd
// package (AVX2 VMINPS/VMAXPS on amd64, NEON on arm64, wasm SIMD128 on
// wasm). Processes a hardware-native width of float32 samples per
// iteration; a Part call handles the remainder.
func clipSamplesF32SIMD(dst, src []float32, count uint64) {
	lo := simd.BroadcastFloat32s(-1)
	hi := simd.BroadcastFloat32s(1)
	width := lo.Len()
	n := int(count)
	i := 0
	for ; i+width <= n; i += width {
		simd.LoadFloat32s(src[i:]).Max(lo).Min(hi).Store(dst[i:])
	}
	if i < n {
		v, _ := simd.LoadFloat32sPart(src[i:])
		v.Max(lo).Min(hi).StorePart(dst[i:])
	}
}

// copyApplyVolumeF32SIMD multiplies samples by factor using the portable
// simd package. Processes a hardware-native width of float32 samples per
// iteration; a Part call handles the remainder.
func copyApplyVolumeF32SIMD(dst, src []float32, count uint64, factor float32) {
	fv := simd.BroadcastFloat32s(factor)
	width := fv.Len()
	n := int(count)
	i := 0
	for ; i+width <= n; i += width {
		simd.LoadFloat32s(src[i:]).Mul(fv).Store(dst[i:])
	}
	if i < n {
		v, _ := simd.LoadFloat32sPart(src[i:])
		v.Mul(fv).StorePart(dst[i:])
	}
}

// copyApplyVolumeAndClipF32SIMD scales then clamps using the portable simd
// package. Processes a hardware-native width of float32 samples per
// iteration; a Part call handles the remainder.
func copyApplyVolumeAndClipF32SIMD(dst, src []float32, count uint64, volume float32) {
	vv := simd.BroadcastFloat32s(volume)
	lo := simd.BroadcastFloat32s(-1)
	hi := simd.BroadcastFloat32s(1)
	width := vv.Len()
	n := int(count)
	i := 0
	for ; i+width <= n; i += width {
		simd.LoadFloat32s(src[i:]).Mul(vv).Max(lo).Min(hi).Store(dst[i:])
	}
	if i < n {
		v, _ := simd.LoadFloat32sPart(src[i:])
		v.Mul(vv).Max(lo).Min(hi).StorePart(dst[i:])
	}
}

// mixPCMFramesF32SIMD computes dst += src * volume using the portable simd
// package's MulAdd, which lowers to a fused multiply-add where the hardware
// supports one. Processes a hardware-native width of float32 samples per
// iteration; a Part call handles the remainder.
func mixPCMFramesF32SIMD(dst, src []float32, sampleCount int, volume float32) {
	vv := simd.BroadcastFloat32s(volume)
	width := vv.Len()
	i := 0
	if volume == 1 {
		for ; i+width <= sampleCount; i += width {
			d := simd.LoadFloat32s(dst[i:])
			s := simd.LoadFloat32s(src[i:])
			d.Add(s).Store(dst[i:])
		}
		if i < sampleCount {
			d, _ := simd.LoadFloat32sPart(dst[i:])
			s, _ := simd.LoadFloat32sPart(src[i:])
			d.Add(s).StorePart(dst[i:])
		}
	} else {
		for ; i+width <= sampleCount; i += width {
			d := simd.LoadFloat32s(dst[i:])
			s := simd.LoadFloat32s(src[i:])
			// d = s*vv + d  (fused multiply-add)
			s.MulAdd(vv, d).Store(dst[i:])
		}
		if i < sampleCount {
			d, _ := simd.LoadFloat32sPart(dst[i:])
			s, _ := simd.LoadFloat32sPart(src[i:])
			s.MulAdd(vv, d).StorePart(dst[i:])
		}
	}
}
