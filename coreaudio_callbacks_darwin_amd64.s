// C-ABI AudioUnit callbacks for the CoreAudio backend. These run on the
// CoreAudio real-time thread and never touch the Go runtime: they copy
// audio between the AudioBufferList and a lock-free ring buffer described
// by a caRingControl block (field offsets documented in
// coreaudio_bindings_darwin.go), then signal a mach semaphore to wake the
// Go feeder goroutine.
//
// System V AMD64 ABI: args in DI, SI, DX, CX, R8, R9; return in AX. Only
// caller-saved registers are used (AX, CX, DX, SI, DI, R8-R11). The stack
// is 8 mod 16 at entry (after the return address push); calls re-align it.
//
// AURenderCallback(inRefCon, ioActionFlags, inTimeStamp, inBusNumber,
//                  inNumberFrames, ioData):
//   DI = inRefCon (caRingControl*), R8 = inNumberFrames, R9 = ioData.
//
// AudioBufferList: mNumberBuffers @0; mBuffers[0]: mNumberChannels @8,
// mDataByteSize @12, mData @16.
//
// caRingControl: ringBase @0, ringSize @8, readPos @16, writePos @24,
// sem @32, semSignal @40, renderFn @48, audioUnit @56, bounceABL @64,
// bpf @72, underruns @76.

//go:build darwin && amd64

#include "textflag.h"

// Playback: fill ioData from the ring, zero-fill on underrun, bump
// readPos, signal the feeder semaphore.
TEXT ca_playback_cb<>(SB),NOSPLIT,$0-0
	MOVQ	DI, R8            // R8 = ctrl (frees DI; frame count not needed)
	MOVL	12(R9), DX        // DX = wanted bytes
	MOVQ	16(R9), R11       // R11 = dst
	MOVQ	8(R8), R9         // R9 = ringSize (ioData no longer needed)

	// R10 = toCopy = min(wanted, writePos - readPos).
	MOVQ	24(R8), AX
	SUBQ	16(R8), AX
	MOVQ	DX, R10
	CMPQ	AX, R10
	JAE	pb_min_done
	MOVQ	AX, R10
pb_min_done:

	// AX = ring offset = readPos & (ringSize - 1).
	MOVQ	R9, AX
	DECQ	AX
	ANDQ	16(R8), AX

	// CX = seg1 = min(toCopy, ringSize - offset).
	MOVQ	R9, CX
	SUBQ	AX, CX
	CMPQ	CX, R10
	JBE	pb_seg1_done
	MOVQ	R10, CX
pb_seg1_done:

	// Copy seg1 from ringBase+offset to dst.
	MOVQ	0(R8), SI
	ADDQ	AX, SI
	MOVQ	R11, DI
	MOVQ	CX, AX            // AX = seg1 (survives REP MOVSB)
	REP;	MOVSB

	// Copy seg2 = toCopy - seg1 from ringBase (wraparound).
	MOVQ	R10, CX
	SUBQ	AX, CX
	MOVQ	0(R8), SI
	REP;	MOVSB

	// Zero-fill the underrun tail: wanted - toCopy bytes.
	MOVQ	DX, CX
	SUBQ	R10, CX
	JZ	pb_no_underrun
	INCL	76(R8)            // underruns++
	XORL	AX, AX
	REP;	STOSB
pb_no_underrun:

	// readPos += toCopy (single consumer; aligned store is atomic).
	MOVQ	16(R8), AX
	ADDQ	R10, AX
	MOVQ	AX, 16(R8)

	// semaphore_signal(ctrl->sem).
	MOVL	32(R8), DI
	MOVQ	40(R8), AX
	SUBQ	$8, SP
	CALL	AX
	ADDQ	$8, SP

	XORL	AX, AX
	RET

GLOBL	·caPlaybackCallbackPtr(SB), RODATA, $8
DATA	·caPlaybackCallbackPtr(SB)/8, $ca_playback_cb<>(SB)

// Capture: pull frames from the device with AudioUnitRender into the
// bounce buffer, copy them into the ring, bump writePos, signal.
TEXT ca_capture_cb<>(SB),NOSPLIT,$0-0
	SUBQ	$24, SP           // Align (entry SP%16 == 8) and reserve.
	MOVQ	DI, 0(SP)         // Save ctrl.

	// bounceABL->mBuffers[0].mDataByteSize = frames * bpf.
	MOVQ	64(DI), R10       // ABL
	MOVL	72(DI), AX        // bpf
	IMULL	R8, AX
	MOVL	AX, 12(R10)

	// AudioUnitRender(unit, ioActionFlags, inTimeStamp, inBusNumber,
	//                 inNumberFrames, bounceABL).
	MOVQ	48(DI), R11       // renderFn
	MOVQ	56(DI), AX        // unit
	MOVQ	R10, R9           // ABL -> arg 6
	MOVQ	AX, DI            // unit -> arg 1; SI/DX/CX/R8 pass through
	CALL	R11
	TESTL	AX, AX
	JNE	cap_done          // Render failed: report the status.

	MOVQ	0(SP), R8         // R8 = ctrl.
	MOVQ	64(R8), R10       // ABL
	MOVL	12(R10), DX       // DX = captured bytes
	MOVQ	16(R10), R11      // R11 = src (ABL's mData)
	MOVQ	8(R8), R9         // R9 = ringSize

	// R10 = toCopy = min(captured, ringSize - (writePos - readPos)).
	MOVQ	24(R8), AX
	SUBQ	16(R8), AX        // used
	MOVQ	R9, CX
	SUBQ	AX, CX            // free
	MOVQ	DX, R10
	CMPQ	CX, R10
	JAE	cap_min_done
	MOVQ	CX, R10           // Overrun: drop the excess.
	INCL	76(R8)
cap_min_done:

	// AX = ring offset = writePos & (ringSize - 1).
	MOVQ	R9, AX
	DECQ	AX
	ANDQ	24(R8), AX

	// CX = seg1 = min(toCopy, ringSize - offset).
	MOVQ	R9, CX
	SUBQ	AX, CX
	CMPQ	CX, R10
	JBE	cap_seg1_done
	MOVQ	R10, CX
cap_seg1_done:

	// Copy seg1 into ringBase+offset.
	MOVQ	0(R8), DI
	ADDQ	AX, DI
	MOVQ	R11, SI
	MOVQ	CX, AX            // AX = seg1
	REP;	MOVSB

	// Copy seg2 = toCopy - seg1 to ringBase (wraparound).
	MOVQ	R10, CX
	SUBQ	AX, CX
	MOVQ	0(R8), DI
	REP;	MOVSB

	// writePos += toCopy.
	MOVQ	24(R8), AX
	ADDQ	R10, AX
	MOVQ	AX, 24(R8)

	// semaphore_signal(ctrl->sem).
	MOVL	32(R8), DI
	MOVQ	40(R8), AX
	CALL	AX

	XORL	AX, AX
cap_done:
	ADDQ	$24, SP
	RET

GLOBL	·caCaptureCallbackPtr(SB), RODATA, $8
DATA	·caCaptureCallbackPtr(SB)/8, $ca_capture_cb<>(SB)
