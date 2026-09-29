//go:build goexperiment.simd

package gominiaudio

import "simd"

func init() {
	// See volume_simd.go for why the emulated path is skipped.
	if simd.Emulated() {
		return
	}
	lerpChannelsImpl = lerpChannelsSIMD
}

// lerpChannelsSIMD computes dst[c] = x0[c] + (x1[c] - x0[c]) * a
// for each channel using the portable simd package's MulAdd. The blend
// coefficient a is identical across all channels so it is broadcast once
// and used with a fused multiply-add.
//
// Equivalent to: dst = x0 + (x1 - x0) * a = x0*(1-a) + x1*a
// Using MulAdd:  diff = x1 - x0; dst = diff * a + x0
//
// Processes a hardware-native width of channels per iteration; a Part call
// handles the remainder.
func lerpChannelsSIMD(dst, x0, x1 []float32, a float32, ch int) {
	av := simd.BroadcastFloat32s(a)
	width := av.Len()
	i := 0
	for ; i+width <= ch; i += width {
		v0 := simd.LoadFloat32s(x0[i:])
		v1 := simd.LoadFloat32s(x1[i:])
		diff := v1.Sub(v0)
		// dst = diff * a + x0  (FMA: diff.MulAdd(av, v0))
		diff.MulAdd(av, v0).Store(dst[i:])
	}
	if i < ch {
		v0, _ := simd.LoadFloat32sPart(x0[i:])
		v1, _ := simd.LoadFloat32sPart(x1[i:])
		diff := v1.Sub(v0)
		diff.MulAdd(av, v0).StorePart(dst[i:])
	}
}
