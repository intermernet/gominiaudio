//go:build goexperiment.simd && amd64

package gominiaudio

import (
	"simd/archsimd"
	"unsafe"
)

func init() {
	if !archsimd.X86.AVX2() {
		return
	}
	convertF32ToS16Impl = convertF32ToS16SIMD
	convertS16ToF32Impl = convertS16ToF32SIMD
}

// s16PackQuadOrder reorders the 64-bit quads produced by VPACKSSDW so the
// eight distinct int16 results land contiguously. VPACKSSDW packs each
// 128-bit lane independently, so SaturateToInt16ConcatGrouped(i32, i32)
// yields quads [in0..3, in0..3, in4..7, in4..7]; permuting to [0,2,1,3]
// puts [in0..3, in4..7] in the low 128 bits, which GetLo then extracts.
var s16PackQuadOrder = [4]uint64{0, 2, 1, 3}

// convertF32ToS16SIMD converts float32 samples to int16 using AVX2.
// Processes 8 samples per iteration using VCVTTPS2DQ + VPACKSSDW with
// saturation; scalar tail handles remainders.
//
// The destination byte slice is reinterpreted as int16 (all supported
// targets are little-endian) so the vector stores directly, with no
// per-element repacking.
func convertF32ToS16SIMD(dst []byte, src []float32, n int) {
	scale := archsimd.BroadcastFloat32x8(32767.0)
	lo := archsimd.BroadcastFloat32x8(-1)
	hi := archsimd.BroadcastFloat32x8(1)
	quadOrder := archsimd.LoadUint64x4(&s16PackQuadOrder)

	i := 0
	if n >= 8 {
		d16 := bytesToS16(dst)
		for ; i <= n-8; i += 8 {
			// Clamp to [-1, 1], scale, truncate float32 → int32.
			v := archsimd.LoadFloat32x8Slice(src[i:]).Max(lo).Min(hi).Mul(scale)
			i32 := v.ConvertToInt32()
			// Pack int32 → int16 with signed saturation. VPACKSSDW works
			// per-128-bit-lane, so reorder the 64-bit quads before taking
			// the low half to get the 8 contiguous results.
			packed := i32.SaturateToInt16ConcatGrouped(i32)
			ordered := packed.AsUint64x4().Permute(quadOrder).AsInt16x16()
			ordered.GetLo().Store((*[8]int16)(unsafe.Pointer(&d16[i])))
		}
	}
	// Scalar tail.
	convertF32ToS16Scalar(dst[i*2:], src[i:], n-i)
}

// convertS16ToF32SIMD converts int16 samples to float32 using AVX2.
// Processes 8 samples per iteration using VPMOVSXWD (sign-extend i16→i32)
// followed by VCVTDQ2PS and a multiply; scalar tail handles remainders.
//
// The source byte slice is reinterpreted as int16 (little-endian targets
// only) so the vector loads directly, with no per-element gathering.
func convertS16ToF32SIMD(dst []float32, src []byte, n int) {
	inv := archsimd.BroadcastFloat32x8(1.0 / 32768.0)

	i := 0
	if n >= 8 {
		s16 := bytesToS16(src)
		for ; i <= n-8; i += 8 {
			i16vec := archsimd.LoadInt16x8((*[8]int16)(unsafe.Pointer(&s16[i])))
			i16vec.ExtendToInt32().ConvertToFloat32().Mul(inv).StoreSlice(dst[i:])
		}
	}
	// Scalar tail.
	convertS16ToF32Scalar(dst[i:], src[i*2:], n-i)
}
