//go:build goexperiment.simd && amd64

package gominiaudio

import "simd/archsimd"

func init() {
	// Only enable SIMD paths when the required CPU features are present.
	if !archsimd.X86.AVX2() {
		return
	}
	clipSamplesF32Impl = clipSamplesF32SIMD
	copyApplyVolumeF32Impl = copyApplyVolumeF32SIMD
	copyApplyVolumeAndClipF32Impl = copyApplyVolumeAndClipF32SIMD
	if archsimd.X86.FMA() {
		mixPCMFramesF32Impl = mixPCMFramesF32SIMD
	}
}

// clipSamplesF32SIMD clamps samples to [-1, 1] using AVX2 VMINPS/VMAXPS.
// Processes 8 float32 samples per iteration; scalar tail handles remainders.
func clipSamplesF32SIMD(dst, src []float32, count uint64) {
	lo := archsimd.BroadcastFloat32x8(-1)
	hi := archsimd.BroadcastFloat32x8(1)
	n := int(count)
	i := 0
	for ; i <= n-8; i += 8 {
		v := archsimd.LoadFloat32x8Slice(src[i:])
		v = v.Max(lo).Min(hi)
		v.StoreSlice(dst[i:])
	}
	// Scalar tail.
	for ; i < n; i++ {
		x := src[i]
		if x < -1 {
			x = -1
		} else if x > 1 {
			x = 1
		}
		dst[i] = x
	}
}

// copyApplyVolumeF32SIMD multiplies samples by factor using AVX2 VMULPS.
// Processes 8 float32 samples per iteration; scalar tail handles remainders.
func copyApplyVolumeF32SIMD(dst, src []float32, count uint64, factor float32) {
	fv := archsimd.BroadcastFloat32x8(factor)
	n := int(count)
	i := 0
	for ; i <= n-8; i += 8 {
		archsimd.LoadFloat32x8Slice(src[i:]).Mul(fv).StoreSlice(dst[i:])
	}
	for ; i < n; i++ {
		dst[i] = src[i] * factor
	}
}

// copyApplyVolumeAndClipF32SIMD scales then clamps using AVX2.
// Processes 8 float32 samples per iteration; scalar tail handles remainders.
func copyApplyVolumeAndClipF32SIMD(dst, src []float32, count uint64, volume float32) {
	vv := archsimd.BroadcastFloat32x8(volume)
	lo := archsimd.BroadcastFloat32x8(-1)
	hi := archsimd.BroadcastFloat32x8(1)
	n := int(count)
	i := 0
	for ; i <= n-8; i += 8 {
		v := archsimd.LoadFloat32x8Slice(src[i:]).Mul(vv)
		v = v.Max(lo).Min(hi)
		v.StoreSlice(dst[i:])
	}
	for ; i < n; i++ {
		x := src[i] * volume
		if x < -1 {
			x = -1
		} else if x > 1 {
			x = 1
		}
		dst[i] = x
	}
}

// mixPCMFramesF32SIMD computes dst += src * volume using AVX2 FMA.
// Uses MulAdd (VFMADD213PS) to fuse the multiply and add in one instruction.
// Processes 8 float32 samples per iteration; scalar tail handles remainders.
func mixPCMFramesF32SIMD(dst, src []float32, sampleCount int, volume float32) {
	vv := archsimd.BroadcastFloat32x8(volume)
	i := 0
	if volume == 1 {
		for ; i <= sampleCount-8; i += 8 {
			d := archsimd.LoadFloat32x8Slice(dst[i:])
			s := archsimd.LoadFloat32x8Slice(src[i:])
			d.Add(s).StoreSlice(dst[i:])
		}
		for ; i < sampleCount; i++ {
			dst[i] += src[i]
		}
	} else {
		for ; i <= sampleCount-8; i += 8 {
			d := archsimd.LoadFloat32x8Slice(dst[i:])
			s := archsimd.LoadFloat32x8Slice(src[i:])
			// d = s*vv + d  (fused multiply-add)
			s.MulAdd(vv, d).StoreSlice(dst[i:])
		}
		for ; i < sampleCount; i++ {
			dst[i] += src[i] * volume
		}
	}
}
