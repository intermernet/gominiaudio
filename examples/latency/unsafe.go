package main

import "unsafe"

// unsafeF32 reinterprets a byte slice as float32 samples without copying.
func unsafeF32(b []byte) []float32 {
	return unsafe.Slice((*float32)(unsafe.Pointer(&b[0])), len(b)/4)
}
