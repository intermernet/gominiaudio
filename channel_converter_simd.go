//go:build goexperiment.simd

package gominiaudio

import "simd"

func init() {
	// See volume_simd.go for why the emulated path is skipped.
	if simd.Emulated() {
		return
	}
	channelConvertWeightsImpl = channelConvertWeightsSIMD
}

// channelConvertWeightsSIMD mixes channels using the weight matrix with the
// portable simd package's MulAdd. For each output frame it accumulates
//
//	dst[f*cout+o] = Σ_i  src[f*cin+i] * weights[i][o]
//
// Strategy: process a hardware-native width of output channels per
// iteration. For each input channel i, broadcast src[i] and fuse-multiply-
// add the corresponding weights[i][o:o+width] into the accumulator. A Part
// call handles cout not divisible by width.
func channelConvertWeightsSIMD(dst, src []float32, weights [][]float32, n, cin, cout int) {
	width := simd.BroadcastFloat32s(0).Len()
	for f := 0; f < n; f++ {
		srcBase := f * cin
		dstBase := f * cout

		o := 0
		for ; o+width <= cout; o += width {
			// Accumulate over all input channels.
			acc := simd.BroadcastFloat32s(0)
			for i := 0; i < cin; i++ {
				sx := simd.BroadcastFloat32s(src[srcBase+i])
				// weights[i] is a slice of length cout; elements [o:o+width] are consecutive.
				w := simd.LoadFloat32s(weights[i][o:])
				// acc += src[i] * w  (fused multiply-add)
				acc = sx.MulAdd(w, acc)
			}
			acc.Store(dst[dstBase+o:])
		}
		if o < cout {
			acc := simd.BroadcastFloat32s(0)
			for i := 0; i < cin; i++ {
				sx := simd.BroadcastFloat32s(src[srcBase+i])
				w, _ := simd.LoadFloat32sPart(weights[i][o:])
				acc = sx.MulAdd(w, acc)
			}
			acc.StorePart(dst[dstBase+o:])
		}
	}
}
