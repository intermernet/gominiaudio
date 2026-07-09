package gominiaudio

import (
	"syscall"
	"unsafe"
	_ "unsafe" // for go:linkname
)

// CoreAudio bindings without cgo. Symbols are bound with
// //go:cgo_import_dynamic (the same technique golang.org/x/sys/unix uses on
// darwin) and called through the runtime's libcCall mechanism via
// syscall.syscall/syscall6.

//go:linkname syscall_syscall syscall.syscall
func syscall_syscall(fn, a1, a2, a3 uintptr) (r1, r2 uintptr, err syscall.Errno)

//go:linkname syscall_syscall6 syscall.syscall6
func syscall_syscall6(fn, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err syscall.Errno)

// CoreAudio (HAL).
//go:cgo_import_dynamic libc_AudioObjectGetPropertyData AudioObjectGetPropertyData "/System/Library/Frameworks/CoreAudio.framework/Versions/A/CoreAudio"
//go:cgo_import_dynamic libc_AudioObjectGetPropertyDataSize AudioObjectGetPropertyDataSize "/System/Library/Frameworks/CoreAudio.framework/Versions/A/CoreAudio"
//go:cgo_import_dynamic libc_AudioObjectSetPropertyData AudioObjectSetPropertyData "/System/Library/Frameworks/CoreAudio.framework/Versions/A/CoreAudio"

// AudioToolbox (AudioUnit / AudioComponent).
//go:cgo_import_dynamic libc_AudioComponentFindNext AudioComponentFindNext "/System/Library/Frameworks/AudioToolbox.framework/Versions/A/AudioToolbox"
//go:cgo_import_dynamic libc_AudioComponentInstanceNew AudioComponentInstanceNew "/System/Library/Frameworks/AudioToolbox.framework/Versions/A/AudioToolbox"
//go:cgo_import_dynamic libc_AudioComponentInstanceDispose AudioComponentInstanceDispose "/System/Library/Frameworks/AudioToolbox.framework/Versions/A/AudioToolbox"
//go:cgo_import_dynamic libc_AudioUnitSetProperty AudioUnitSetProperty "/System/Library/Frameworks/AudioToolbox.framework/Versions/A/AudioToolbox"
//go:cgo_import_dynamic libc_AudioUnitGetProperty AudioUnitGetProperty "/System/Library/Frameworks/AudioToolbox.framework/Versions/A/AudioToolbox"
//go:cgo_import_dynamic libc_AudioUnitInitialize AudioUnitInitialize "/System/Library/Frameworks/AudioToolbox.framework/Versions/A/AudioToolbox"
//go:cgo_import_dynamic libc_AudioUnitUninitialize AudioUnitUninitialize "/System/Library/Frameworks/AudioToolbox.framework/Versions/A/AudioToolbox"
//go:cgo_import_dynamic libc_AudioOutputUnitStart AudioOutputUnitStart "/System/Library/Frameworks/AudioToolbox.framework/Versions/A/AudioToolbox"
//go:cgo_import_dynamic libc_AudioOutputUnitStop AudioOutputUnitStop "/System/Library/Frameworks/AudioToolbox.framework/Versions/A/AudioToolbox"
//go:cgo_import_dynamic libc_AudioUnitRender AudioUnitRender "/System/Library/Frameworks/AudioToolbox.framework/Versions/A/AudioToolbox"

// CoreFoundation (device name strings).
//go:cgo_import_dynamic libc_CFStringGetCString CFStringGetCString "/System/Library/Frameworks/CoreFoundation.framework/Versions/A/CoreFoundation"
//go:cgo_import_dynamic libc_CFRelease CFRelease "/System/Library/Frameworks/CoreFoundation.framework/Versions/A/CoreFoundation"

// libSystem (mach semaphores for callback->feeder signaling).
//go:cgo_import_dynamic libc_mach_task_self mach_task_self "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_semaphore_create semaphore_create "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_semaphore_signal semaphore_signal "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_semaphore_wait semaphore_wait "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_semaphore_destroy semaphore_destroy "/usr/lib/libSystem.B.dylib"

// Trampoline addresses, defined in coreaudio_trampolines_darwin.s.
var libc_AudioObjectGetPropertyData_trampoline_addr uintptr
var libc_AudioObjectGetPropertyDataSize_trampoline_addr uintptr
var libc_AudioObjectSetPropertyData_trampoline_addr uintptr
var libc_AudioComponentFindNext_trampoline_addr uintptr
var libc_AudioComponentInstanceNew_trampoline_addr uintptr
var libc_AudioComponentInstanceDispose_trampoline_addr uintptr
var libc_AudioUnitSetProperty_trampoline_addr uintptr
var libc_AudioUnitGetProperty_trampoline_addr uintptr
var libc_AudioUnitInitialize_trampoline_addr uintptr
var libc_AudioUnitUninitialize_trampoline_addr uintptr
var libc_AudioOutputUnitStart_trampoline_addr uintptr
var libc_AudioOutputUnitStop_trampoline_addr uintptr
var libc_AudioUnitRender_trampoline_addr uintptr
var libc_CFStringGetCString_trampoline_addr uintptr
var libc_CFRelease_trampoline_addr uintptr
var libc_mach_task_self_trampoline_addr uintptr
var libc_semaphore_create_trampoline_addr uintptr
var libc_semaphore_signal_trampoline_addr uintptr
var libc_semaphore_wait_trampoline_addr uintptr
var libc_semaphore_destroy_trampoline_addr uintptr

// Raw C entry points used by the assembly callbacks (not trampolines: the
// callbacks jump straight to the resolved symbols).
var libc_semaphore_signal_addr uintptr // Set at init from the trampoline.
var libc_AudioUnitRender_addr uintptr

// Callback entry points, defined in coreaudio_callbacks_darwin_*.s. These
// are C-ABI functions that never touch the Go runtime.
var caPlaybackCallbackPtr uintptr
var caCaptureCallbackPtr uintptr

func init() {
	libc_semaphore_signal_addr = libc_semaphore_signal_trampoline_addr
	libc_AudioUnitRender_addr = libc_AudioUnitRender_trampoline_addr
}

/**************************************************************************
CoreAudio types and constants
**************************************************************************/

func fourCC(s string) uint32 {
	return uint32(s[0])<<24 | uint32(s[1])<<16 | uint32(s[2])<<8 | uint32(s[3])
}

var (
	kAudioObjectSystemObject                   = uint32(1)
	kAudioHardwarePropertyDevices              = fourCC("dev#")
	kAudioHardwarePropertyDefaultInputDevice   = fourCC("dIn ")
	kAudioHardwarePropertyDefaultOutputDevice  = fourCC("dOut")
	kAudioObjectPropertyScopeGlobal            = fourCC("glob")
	kAudioObjectPropertyScopeInput             = fourCC("inpt")
	kAudioObjectPropertyScopeOutput            = fourCC("outp")
	kAudioObjectPropertyName                   = fourCC("lnam")
	kAudioDevicePropertyStreams                = fourCC("stm#")
	kAudioDevicePropertyNominalSampleRate      = fourCC("nsrt")
	kAudioDevicePropertyStreamConfiguration    = fourCC("slay")
	kAudioDevicePropertyBufferFrameSize        = fourCC("fsiz")
	kAudioDevicePropertyDeviceUID              = fourCC("uid ")

	kAudioUnitType_Output          = fourCC("auou")
	kAudioUnitSubType_HALOutput    = fourCC("ahal")
	kAudioUnitManufacturer_Apple   = fourCC("appl")

	kAudioFormatLinearPCM = fourCC("lpcm")
)

const (
	kAudioObjectPropertyElementMain = 0

	kAudioUnitProperty_StreamFormat          = 8
	kAudioUnitProperty_MaximumFramesPerSlice = 14
	kAudioUnitProperty_SetRenderCallback     = 23
	kAudioOutputUnitProperty_CurrentDevice   = 2000
	kAudioOutputUnitProperty_EnableIO        = 2003
	kAudioOutputUnitProperty_SetInputCallback = 2005

	kAudioUnitScope_Global = 0
	kAudioUnitScope_Input  = 1
	kAudioUnitScope_Output = 2

	kAudioFormatFlagIsFloat  = 1 << 0
	kAudioFormatFlagIsPacked = 1 << 3

	kCFStringEncodingUTF8 = 0x08000100

	syncPolicyFIFO = 0
)

// audioObjectPropertyAddress mirrors AudioObjectPropertyAddress.
type audioObjectPropertyAddress struct {
	Selector uint32
	Scope    uint32
	Element  uint32
}

// audioStreamBasicDescription mirrors AudioStreamBasicDescription.
type audioStreamBasicDescription struct {
	SampleRate       float64
	FormatID         uint32
	FormatFlags      uint32
	BytesPerPacket   uint32
	FramesPerPacket  uint32
	BytesPerFrame    uint32
	ChannelsPerFrame uint32
	BitsPerChannel   uint32
	_                uint32
}

// audioComponentDescription mirrors AudioComponentDescription.
type audioComponentDescription struct {
	Type         uint32
	SubType      uint32
	Manufacturer uint32
	Flags        uint32
	FlagsMask    uint32
}

// auRenderCallbackStruct mirrors AURenderCallbackStruct.
type auRenderCallbackStruct struct {
	Proc    uintptr
	RefCon  uintptr
}

// caRingControl is the shared control block between Go and the assembly
// callbacks. Field offsets are hardcoded in coreaudio_callbacks_darwin_*.s:
//
//	 0  ringBase   uintptr
//	 8  ringSize   uint64 (bytes, power of two)
//	16  readPos    uint64 (byte counter)
//	24  writePos   uint64 (byte counter)
//	32  sem        uint32 (semaphore_t)
//	40  semSignal  uintptr (C entry of semaphore_signal)
//	48  renderFn   uintptr (C entry of AudioUnitRender; capture only)
//	56  audioUnit  uintptr (capture only)
//	64  bounceABL  uintptr (capture only)
//	72  bpf        uint32
//	76  underruns  uint32
type caRingControl struct {
	ringBase  uintptr
	ringSize  uint64
	readPos   uint64
	writePos  uint64
	sem       uint32
	_         uint32
	semSignal uintptr
	renderFn  uintptr
	audioUnit uintptr
	bounceABL uintptr
	bpf       uint32
	underruns uint32
}

/**************************************************************************
Call wrappers
**************************************************************************/

func caGetPropertyData(objectID uint32, addr *audioObjectPropertyAddress, dataSize *uint32, data unsafe.Pointer) int32 {
	r1, _, _ := syscall_syscall6(libc_AudioObjectGetPropertyData_trampoline_addr,
		uintptr(objectID), uintptr(unsafe.Pointer(addr)), 0, 0,
		uintptr(unsafe.Pointer(dataSize)), uintptr(data))
	return int32(r1)
}

func caGetPropertyDataSize(objectID uint32, addr *audioObjectPropertyAddress, dataSize *uint32) int32 {
	r1, _, _ := syscall_syscall6(libc_AudioObjectGetPropertyDataSize_trampoline_addr,
		uintptr(objectID), uintptr(unsafe.Pointer(addr)), 0, 0,
		uintptr(unsafe.Pointer(dataSize)), 0)
	return int32(r1)
}

func caSetPropertyData(objectID uint32, addr *audioObjectPropertyAddress, dataSize uint32, data unsafe.Pointer) int32 {
	r1, _, _ := syscall_syscall6(libc_AudioObjectSetPropertyData_trampoline_addr,
		uintptr(objectID), uintptr(unsafe.Pointer(addr)), 0, 0,
		uintptr(dataSize), uintptr(data))
	return int32(r1)
}

func caComponentFindNext(inComponent uintptr, desc *audioComponentDescription) uintptr {
	r1, _, _ := syscall_syscall(libc_AudioComponentFindNext_trampoline_addr,
		inComponent, uintptr(unsafe.Pointer(desc)), 0)
	return r1
}

func caComponentInstanceNew(component uintptr, out *uintptr) int32 {
	r1, _, _ := syscall_syscall(libc_AudioComponentInstanceNew_trampoline_addr,
		component, uintptr(unsafe.Pointer(out)), 0)
	return int32(r1)
}

func caComponentInstanceDispose(unit uintptr) int32 {
	r1, _, _ := syscall_syscall(libc_AudioComponentInstanceDispose_trampoline_addr, unit, 0, 0)
	return int32(r1)
}

func caUnitSetProperty(unit uintptr, propID, scope, element uint32, data unsafe.Pointer, size uint32) int32 {
	r1, _, _ := syscall_syscall6(libc_AudioUnitSetProperty_trampoline_addr,
		unit, uintptr(propID), uintptr(scope), uintptr(element), uintptr(data), uintptr(size))
	return int32(r1)
}

func caUnitGetProperty(unit uintptr, propID, scope, element uint32, data unsafe.Pointer, size *uint32) int32 {
	r1, _, _ := syscall_syscall6(libc_AudioUnitGetProperty_trampoline_addr,
		unit, uintptr(propID), uintptr(scope), uintptr(element), uintptr(data), uintptr(unsafe.Pointer(size)))
	return int32(r1)
}

func caUnitInitialize(unit uintptr) int32 {
	r1, _, _ := syscall_syscall(libc_AudioUnitInitialize_trampoline_addr, unit, 0, 0)
	return int32(r1)
}

func caUnitUninitialize(unit uintptr) int32 {
	r1, _, _ := syscall_syscall(libc_AudioUnitUninitialize_trampoline_addr, unit, 0, 0)
	return int32(r1)
}

func caOutputUnitStart(unit uintptr) int32 {
	r1, _, _ := syscall_syscall(libc_AudioOutputUnitStart_trampoline_addr, unit, 0, 0)
	return int32(r1)
}

func caOutputUnitStop(unit uintptr) int32 {
	r1, _, _ := syscall_syscall(libc_AudioOutputUnitStop_trampoline_addr, unit, 0, 0)
	return int32(r1)
}

func cfStringToGo(cfStr uintptr) string {
	if cfStr == 0 {
		return ""
	}
	buf := make([]byte, 512)
	r1, _, _ := syscall_syscall6(libc_CFStringGetCString_trampoline_addr,
		cfStr, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), kCFStringEncodingUTF8, 0, 0)
	syscall_syscall(libc_CFRelease_trampoline_addr, cfStr, 0, 0)
	if r1 == 0 {
		return ""
	}
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i])
		}
	}
	return string(buf)
}

func machTaskSelf() uintptr {
	r1, _, _ := syscall_syscall(libc_mach_task_self_trampoline_addr, 0, 0, 0)
	return r1
}

func semaphoreCreate() (uint32, int32) {
	var sem uint32
	r1, _, _ := syscall_syscall6(libc_semaphore_create_trampoline_addr,
		machTaskSelf(), uintptr(unsafe.Pointer(&sem)), syncPolicyFIFO, 0, 0, 0)
	return sem, int32(r1)
}

func semaphoreWait(sem uint32) int32 {
	r1, _, _ := syscall_syscall(libc_semaphore_wait_trampoline_addr, uintptr(sem), 0, 0)
	return int32(r1)
}

func semaphoreSignal(sem uint32) int32 {
	r1, _, _ := syscall_syscall(libc_semaphore_signal_trampoline_addr, uintptr(sem), 0, 0)
	return int32(r1)
}

func semaphoreDestroy(sem uint32) int32 {
	r1, _, _ := syscall_syscall(libc_semaphore_destroy_trampoline_addr, machTaskSelf(), uintptr(sem), 0)
	return int32(r1)
}
