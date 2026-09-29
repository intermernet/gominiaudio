//go:build goexperiment.simd

package gominiaudio

import "simd"

func init() {
	// See volume_simd.go for why the emulated path is skipped.
	if simd.Emulated() {
		return
	}
	biquadProcessF32Impl = biquadProcessF32SIMD
}

// biquadProcessF32SIMD processes a Biquad filter (transposed direct form 2)
// using the portable simd package's MulAdd for the inner channel loop.
//
// For each frame f:
//
//	y[c]    = b0 * x[c]  + r1[c]
//	r1'[c]  = b1 * x[c]  - a1 * y[c] + r2[c]
//	r2'[c]  = b2 * x[c]  - a2 * y[c]
//
// The channel dimension is typically small (2–8 channels). When ch is at
// least the hardware's native vector width, the SIMD path processes all ch
// channels in parallel for each frame. Below that, the scalar path is used
// (the LPF is the common case at ch=2, and that path still benefits from
// float32 throughout with no f64 widening).
func biquadProcessF32SIMD(dst, src, r1, r2 []float32, b0, b1, b2, a1, a2 float32, n, ch int) {
	b0v := simd.BroadcastFloat32s(b0)
	width := b0v.Len()
	if ch < width {
		// Scalar path for small channel counts (most audio is ≤ 8 channels).
		// Still uses float32 throughout (no f64 widening).
		biquadProcessF32Scalar(dst, src, r1, r2, b0, b1, b2, a1, a2, n, ch)
		return
	}

	b1v := simd.BroadcastFloat32s(b1)
	b2v := simd.BroadcastFloat32s(b2)
	a1v := simd.BroadcastFloat32s(a1)
	a2v := simd.BroadcastFloat32s(a2)

	for f := 0; f < n; f++ {
		base := f * ch
		c := 0
		for ; c+width <= ch; c += width {
			x := simd.LoadFloat32s(src[base+c:])
			s1 := simd.LoadFloat32s(r1[c:])
			s2 := simd.LoadFloat32s(r2[c:])

			// y = b0*x + r1
			y := b0v.MulAdd(x, s1) // b0*x + s1
			// r1' = b1*x - a1*y + r2
			r1new := b1v.MulAdd(x, s2).Sub(a1v.Mul(y)) // b1*x + s2 - a1*y
			// r2' = b2*x - a2*y
			r2new := b2v.Mul(x).Sub(a2v.Mul(y))

			y.Store(dst[base+c:])
			r1new.Store(r1[c:])
			r2new.Store(r2[c:])
		}
		// Scalar tail for remaining channels.
		for ; c < ch; c++ {
			x := src[base+c]
			y := b0*x + r1[c]
			r1[c] = b1*x - a1*y + r2[c]
			r2[c] = b2*x - a2*y
			dst[base+c] = y
		}
	}
}
