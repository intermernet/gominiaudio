// Trampolines for dynamically imported CoreAudio/AudioToolbox/
// CoreFoundation/libSystem symbols, following the golang.org/x/sys/unix
// pattern for cgo-free dynamic linking on darwin.

//go:build darwin

#include "textflag.h"

TEXT libc_AudioObjectGetPropertyData_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_AudioObjectGetPropertyData(SB)
GLOBL	·libc_AudioObjectGetPropertyData_trampoline_addr(SB), RODATA, $8
DATA	·libc_AudioObjectGetPropertyData_trampoline_addr(SB)/8, $libc_AudioObjectGetPropertyData_trampoline<>(SB)

TEXT libc_AudioObjectGetPropertyDataSize_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_AudioObjectGetPropertyDataSize(SB)
GLOBL	·libc_AudioObjectGetPropertyDataSize_trampoline_addr(SB), RODATA, $8
DATA	·libc_AudioObjectGetPropertyDataSize_trampoline_addr(SB)/8, $libc_AudioObjectGetPropertyDataSize_trampoline<>(SB)

TEXT libc_AudioObjectSetPropertyData_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_AudioObjectSetPropertyData(SB)
GLOBL	·libc_AudioObjectSetPropertyData_trampoline_addr(SB), RODATA, $8
DATA	·libc_AudioObjectSetPropertyData_trampoline_addr(SB)/8, $libc_AudioObjectSetPropertyData_trampoline<>(SB)

TEXT libc_AudioComponentFindNext_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_AudioComponentFindNext(SB)
GLOBL	·libc_AudioComponentFindNext_trampoline_addr(SB), RODATA, $8
DATA	·libc_AudioComponentFindNext_trampoline_addr(SB)/8, $libc_AudioComponentFindNext_trampoline<>(SB)

TEXT libc_AudioComponentInstanceNew_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_AudioComponentInstanceNew(SB)
GLOBL	·libc_AudioComponentInstanceNew_trampoline_addr(SB), RODATA, $8
DATA	·libc_AudioComponentInstanceNew_trampoline_addr(SB)/8, $libc_AudioComponentInstanceNew_trampoline<>(SB)

TEXT libc_AudioComponentInstanceDispose_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_AudioComponentInstanceDispose(SB)
GLOBL	·libc_AudioComponentInstanceDispose_trampoline_addr(SB), RODATA, $8
DATA	·libc_AudioComponentInstanceDispose_trampoline_addr(SB)/8, $libc_AudioComponentInstanceDispose_trampoline<>(SB)

TEXT libc_AudioUnitSetProperty_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_AudioUnitSetProperty(SB)
GLOBL	·libc_AudioUnitSetProperty_trampoline_addr(SB), RODATA, $8
DATA	·libc_AudioUnitSetProperty_trampoline_addr(SB)/8, $libc_AudioUnitSetProperty_trampoline<>(SB)

TEXT libc_AudioUnitGetProperty_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_AudioUnitGetProperty(SB)
GLOBL	·libc_AudioUnitGetProperty_trampoline_addr(SB), RODATA, $8
DATA	·libc_AudioUnitGetProperty_trampoline_addr(SB)/8, $libc_AudioUnitGetProperty_trampoline<>(SB)

TEXT libc_AudioUnitInitialize_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_AudioUnitInitialize(SB)
GLOBL	·libc_AudioUnitInitialize_trampoline_addr(SB), RODATA, $8
DATA	·libc_AudioUnitInitialize_trampoline_addr(SB)/8, $libc_AudioUnitInitialize_trampoline<>(SB)

TEXT libc_AudioUnitUninitialize_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_AudioUnitUninitialize(SB)
GLOBL	·libc_AudioUnitUninitialize_trampoline_addr(SB), RODATA, $8
DATA	·libc_AudioUnitUninitialize_trampoline_addr(SB)/8, $libc_AudioUnitUninitialize_trampoline<>(SB)

TEXT libc_AudioOutputUnitStart_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_AudioOutputUnitStart(SB)
GLOBL	·libc_AudioOutputUnitStart_trampoline_addr(SB), RODATA, $8
DATA	·libc_AudioOutputUnitStart_trampoline_addr(SB)/8, $libc_AudioOutputUnitStart_trampoline<>(SB)

TEXT libc_AudioOutputUnitStop_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_AudioOutputUnitStop(SB)
GLOBL	·libc_AudioOutputUnitStop_trampoline_addr(SB), RODATA, $8
DATA	·libc_AudioOutputUnitStop_trampoline_addr(SB)/8, $libc_AudioOutputUnitStop_trampoline<>(SB)

TEXT libc_AudioUnitRender_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_AudioUnitRender(SB)
GLOBL	·libc_AudioUnitRender_trampoline_addr(SB), RODATA, $8
DATA	·libc_AudioUnitRender_trampoline_addr(SB)/8, $libc_AudioUnitRender_trampoline<>(SB)

TEXT libc_CFStringGetCString_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_CFStringGetCString(SB)
GLOBL	·libc_CFStringGetCString_trampoline_addr(SB), RODATA, $8
DATA	·libc_CFStringGetCString_trampoline_addr(SB)/8, $libc_CFStringGetCString_trampoline<>(SB)

TEXT libc_CFRelease_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_CFRelease(SB)
GLOBL	·libc_CFRelease_trampoline_addr(SB), RODATA, $8
DATA	·libc_CFRelease_trampoline_addr(SB)/8, $libc_CFRelease_trampoline<>(SB)

TEXT libc_mach_task_self_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_mach_task_self(SB)
GLOBL	·libc_mach_task_self_trampoline_addr(SB), RODATA, $8
DATA	·libc_mach_task_self_trampoline_addr(SB)/8, $libc_mach_task_self_trampoline<>(SB)

TEXT libc_semaphore_create_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_semaphore_create(SB)
GLOBL	·libc_semaphore_create_trampoline_addr(SB), RODATA, $8
DATA	·libc_semaphore_create_trampoline_addr(SB)/8, $libc_semaphore_create_trampoline<>(SB)

TEXT libc_semaphore_signal_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_semaphore_signal(SB)
GLOBL	·libc_semaphore_signal_trampoline_addr(SB), RODATA, $8
DATA	·libc_semaphore_signal_trampoline_addr(SB)/8, $libc_semaphore_signal_trampoline<>(SB)

TEXT libc_semaphore_wait_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_semaphore_wait(SB)
GLOBL	·libc_semaphore_wait_trampoline_addr(SB), RODATA, $8
DATA	·libc_semaphore_wait_trampoline_addr(SB)/8, $libc_semaphore_wait_trampoline<>(SB)

TEXT libc_semaphore_destroy_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_semaphore_destroy(SB)
GLOBL	·libc_semaphore_destroy_trampoline_addr(SB), RODATA, $8
DATA	·libc_semaphore_destroy_trampoline_addr(SB)/8, $libc_semaphore_destroy_trampoline<>(SB)
