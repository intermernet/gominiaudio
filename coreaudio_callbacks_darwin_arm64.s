// C-ABI AudioUnit callbacks for the CoreAudio backend (arm64). See the
// amd64 version for the overall design; offsets are documented in
// coreaudio_bindings_darwin.go.
//
// AAPCS64: args in R0-R5, return in R0. Only caller-saved registers
// R0-R15 are used (R18 is platform-reserved). LR is saved around calls.
//
// AURenderCallback(inRefCon, ioActionFlags, inTimeStamp, inBusNumber,
//                  inNumberFrames, ioData):
//   R0 = inRefCon (caRingControl*), R4 = inNumberFrames, R5 = ioData.

//go:build darwin && arm64

#include "textflag.h"

// Playback: fill ioData from the ring, zero-fill on underrun, bump
// readPos, signal the feeder semaphore.
TEXT ca_playback_cb<>(SB),NOSPLIT,$0-0
	MOVD	R0, R6            // ctrl
	MOVWU	12(R5), R9        // wanted bytes
	MOVD	16(R5), R8        // dst
	MOVD	8(R6), R7         // ringSize

	// toCopy (R10) = min(wanted, writePos - readPos).
	MOVD	24(R6), R11
	MOVD	16(R6), R12
	SUB	R12, R11          // avail = write - read
	MOVD	R9, R10
	CMP	R10, R11
	BHS	pb_min_ok         // avail >= toCopy
	MOVD	R11, R10
pb_min_ok:

	// offset (R14) = readPos & (ringSize - 1).
	SUB	$1, R7, R14
	MOVD	16(R6), R12
	AND	R12, R14

	// seg1 (R11) = min(toCopy, ringSize - offset).
	SUB	R14, R7, R11
	CMP	R10, R11
	BLS	pb_seg1_ok        // (size - offset) <= toCopy
	MOVD	R10, R11
pb_seg1_ok:

	// Copy seg1 from ringBase+offset to dst.
	MOVD	0(R6), R12
	ADD	R14, R12
	MOVD	R11, R13
pb_copy1:
	CBZ	R13, pb_copy1_done
	MOVBU.P	1(R12), R15
	MOVB.P	R15, 1(R8)
	SUB	$1, R13
	B	pb_copy1
pb_copy1_done:

	// Copy seg2 = toCopy - seg1 from ringBase (wraparound).
	SUB	R11, R10, R13
	MOVD	0(R6), R12
pb_copy2:
	CBZ	R13, pb_copy2_done
	MOVBU.P	1(R12), R15
	MOVB.P	R15, 1(R8)
	SUB	$1, R13
	B	pb_copy2
pb_copy2_done:

	// Zero-fill the underrun tail: wanted - toCopy bytes.
	SUB	R10, R9, R13
	CBZ	R13, pb_no_underrun
	MOVWU	76(R6), R15
	ADD	$1, R15
	MOVW	R15, 76(R6)
pb_zero:
	CBZ	R13, pb_no_underrun
	MOVB.P	ZR, 1(R8)
	SUB	$1, R13
	B	pb_zero
pb_no_underrun:

	// readPos += toCopy (single consumer; aligned store is atomic).
	MOVD	16(R6), R12
	ADD	R10, R12
	MOVD	R12, 16(R6)

	// semaphore_signal(ctrl->sem).
	SUB	$16, RSP
	MOVD	R30, (RSP)
	MOVWU	32(R6), R0
	MOVD	40(R6), R11
	CALL	(R11)
	MOVD	(RSP), R30
	ADD	$16, RSP

	MOVD	$0, R0
	RET

GLOBL	·caPlaybackCallbackPtr(SB), RODATA, $8
DATA	·caPlaybackCallbackPtr(SB)/8, $ca_playback_cb<>(SB)

// Capture: pull frames from the device with AudioUnitRender into the
// bounce buffer, copy them into the ring, bump writePos, signal.
TEXT ca_capture_cb<>(SB),NOSPLIT,$0-0
	SUB	$32, RSP
	MOVD	R30, (RSP)
	MOVD	R0, 8(RSP)        // Save ctrl.

	// bounceABL->mBuffers[0].mDataByteSize = frames * bpf.
	MOVD	64(R0), R6        // ABL
	MOVWU	72(R0), R7        // bpf
	MULW	R4, R7, R7
	MOVW	R7, 12(R6)

	// AudioUnitRender(unit, ioActionFlags, inTimeStamp, inBusNumber,
	//                 inNumberFrames, bounceABL).
	MOVD	48(R0), R8        // renderFn
	MOVD	56(R0), R9        // unit
	MOVD	R9, R0
	MOVD	R6, R5            // ABL; R1-R4 pass through
	CALL	(R8)
	CBNZW	R0, cap_ret       // Render failed: report the status.

	MOVD	8(RSP), R6        // ctrl
	MOVD	64(R6), R7        // ABL
	MOVWU	12(R7), R9        // captured bytes
	MOVD	16(R7), R12       // src
	MOVD	8(R6), R7         // ringSize

	// toCopy (R10) = min(captured, ringSize - (writePos - readPos)).
	MOVD	24(R6), R10
	MOVD	16(R6), R11
	SUB	R11, R10          // used
	SUB	R10, R7, R11      // free
	MOVD	R9, R10
	CMP	R10, R11
	BHS	cap_min_ok        // free >= toCopy
	MOVD	R11, R10          // Overrun: drop the excess.
	MOVWU	76(R6), R15
	ADD	$1, R15
	MOVW	R15, 76(R6)
cap_min_ok:

	// offset (R14) = writePos & (ringSize - 1).
	SUB	$1, R7, R14
	MOVD	24(R6), R13
	AND	R13, R14

	// seg1 (R11) = min(toCopy, ringSize - offset).
	SUB	R14, R7, R11
	CMP	R10, R11
	BLS	cap_seg1_ok
	MOVD	R10, R11
cap_seg1_ok:

	// Copy seg1 into ringBase+offset.
	MOVD	0(R6), R8
	ADD	R14, R8
	MOVD	R11, R13
cap_copy1:
	CBZ	R13, cap_copy1_done
	MOVBU.P	1(R12), R15
	MOVB.P	R15, 1(R8)
	SUB	$1, R13
	B	cap_copy1
cap_copy1_done:

	// Copy seg2 = toCopy - seg1 to ringBase (wraparound).
	SUB	R11, R10, R13
	MOVD	0(R6), R8
cap_copy2:
	CBZ	R13, cap_copy2_done
	MOVBU.P	1(R12), R15
	MOVB.P	R15, 1(R8)
	SUB	$1, R13
	B	cap_copy2
cap_copy2_done:

	// writePos += toCopy.
	MOVD	24(R6), R13
	ADD	R10, R13
	MOVD	R13, 24(R6)

	// semaphore_signal(ctrl->sem).
	MOVWU	32(R6), R0
	MOVD	40(R6), R11
	CALL	(R11)

	MOVD	$0, R0
cap_ret:
	MOVD	(RSP), R30
	ADD	$32, RSP
	RET

GLOBL	·caCaptureCallbackPtr(SB), RODATA, $8
DATA	·caCaptureCallbackPtr(SB)/8, $ca_capture_cb<>(SB)
