//go:build goexperiment.simd && amd64

package gominiaudio

import "simd/archsimd"

func init() {
	if archsimd.X86.AVX2() && archsimd.X86.FMA() {
		channelConvertWeightsImpl = channelConvertWeightsSIMD
	}
}

// channelConvertWeightsSIMD mixes channels using the weight matrix with AVX2
// FMA instructions. For each output frame it accumulates
//
//	dst[f*cout+o] = Σ_i  src[f*cin+i] * weights[i][o]
//
// Strategy: process 8 output channels per iteration. For each input channel i,
// broadcast src[i] and FMADD the 8 consecutive weights[i][o:o+8] into the
// accumulator. Scalar tail handles cout not divisible by 8.
func channelConvertWeightsSIMD(dst, src []float32, weights [][]float32, n, cin, cout int) {
	for f := 0; f < n; f++ {
		srcBase := f * cin
		dstBase := f * cout

		o := 0
		for ; o <= cout-8; o += 8 {
			// Accumulate over all input channels.
			acc := archsimd.BroadcastFloat32x8(0)
			for i := 0; i < cin; i++ {
				sx := archsimd.BroadcastFloat32x8(src[srcBase+i])
				// weights[i] is a slice of length cout; elements [o:o+8] are consecutive.
				w := archsimd.LoadFloat32x8Slice(weights[i][o:])
				// acc += src[i] * w  (fused multiply-add)
				acc = sx.MulAdd(w, acc)
			}
			acc.StoreSlice(dst[dstBase+o:])
		}
		// Scalar tail for remaining output channels.
		for ; o < cout; o++ {
			var acc float32
			for i := 0; i < cin; i++ {
				acc += src[srcBase+i] * weights[i][o]
			}
			dst[dstBase+o] = acc
		}
	}
}
