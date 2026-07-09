package gominiaudio

import (
	"encoding/binary"
	"unsafe"
)

// loadU32 reads a little-endian u32 at the given byte offset.
func loadU32(b []byte, off int) uint32 {
	return binary.LittleEndian.Uint32(b[off:])
}

// loadU64 reads a little-endian u64 at the given byte offset.
func loadU64(b []byte, off int) uint64 {
	return binary.LittleEndian.Uint64(b[off:])
}

// bytesToF32 reinterprets a byte slice as a float32 slice without copying.
// The slice must be 4-byte aligned, which holds for all Go-allocated slices.
func bytesToF32(b []byte) []float32 {
	if len(b) < 4 {
		return nil
	}
	return unsafe.Slice((*float32)(unsafe.Pointer(&b[0])), len(b)/4)
}

// f32ToBytes reinterprets a float32 slice as a byte slice without copying.
func f32ToBytes(f []float32) []byte {
	if len(f) == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&f[0])), len(f)*4)
}

// bytesToS16 reinterprets a byte slice as an int16 slice without copying.
func bytesToS16(b []byte) []int16 {
	if len(b) < 2 {
		return nil
	}
	return unsafe.Slice((*int16)(unsafe.Pointer(&b[0])), len(b)/2)
}

// s16ToBytes reinterprets an int16 slice as a byte slice without copying.
func s16ToBytes(s []int16) []byte {
	if len(s) == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&s[0])), len(s)*2)
}
