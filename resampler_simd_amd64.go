//go:build goexperiment.simd && amd64

package gominiaudio

import "simd/archsimd"

func init() {
	if archsimd.X86.AVX2() && archsimd.X86.FMA() {
		lerpChannelsImpl = lerpChannelsSIMD
	}
}

// lerpChannelsSIMD computes dst[c] = x0[c] + (x1[c] - x0[c]) * a
// for each channel using AVX2 FMA. The blend coefficient a is identical
// across all channels so it is broadcast once and used with VFMADD.
//
// Equivalent to: dst = x0 + (x1 - x0) * a = x0*(1-a) + x1*a
// Using VFMADD:  diff = x1 - x0; dst = diff * a + x0  (i.e. diff.MulAdd(a, x0))
//
// Processes 8 channels per iteration; scalar tail handles the rest.
func lerpChannelsSIMD(dst, x0, x1 []float32, a float32, ch int) {
	av := archsimd.BroadcastFloat32x8(a)
	i := 0
	for ; i <= ch-8; i += 8 {
		v0 := archsimd.LoadFloat32x8Slice(x0[i:])
		v1 := archsimd.LoadFloat32x8Slice(x1[i:])
		diff := v1.Sub(v0)
		// dst = diff * a + x0  (FMA: diff.MulAdd(av, v0))
		diff.MulAdd(av, v0).StoreSlice(dst[i:])
	}
	// Scalar tail.
	for ; i < ch; i++ {
		dst[i] = x0[i] + (x1[i]-x0[i])*a
	}
}
