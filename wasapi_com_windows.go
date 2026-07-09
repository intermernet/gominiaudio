package gominiaudio

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Minimal COM interop for WASAPI, implemented with raw vtable calls through
// syscall.SyscallN. No cgo.

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

func mkGUID(d1 uint32, d2, d3 uint16, d4 [8]byte) guid {
	return guid{Data1: d1, Data2: d2, Data3: d3, Data4: d4}
}

var (
	clsidMMDeviceEnumerator = mkGUID(0xBCDE0395, 0xE52F, 0x467C, [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E})
	iidIMMDeviceEnumerator  = mkGUID(0xA95664D2, 0x9614, 0x4F35, [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6})
	iidIAudioClient         = mkGUID(0x1CB9AD4C, 0xDBFA, 0x4C32, [8]byte{0xB1, 0x78, 0xC2, 0xF5, 0x68, 0xA7, 0x03, 0xB2})
	iidIAudioClient3        = mkGUID(0x7ED4EE07, 0x8E67, 0x4CD4, [8]byte{0x8C, 0x1A, 0x2B, 0x7A, 0x59, 0x87, 0xAD, 0x42})
	iidIAudioRenderClient   = mkGUID(0xF294ACFC, 0x3146, 0x4483, [8]byte{0xA7, 0xBF, 0xAD, 0xDC, 0xA7, 0xC2, 0x60, 0xE2})
	iidIAudioCaptureClient  = mkGUID(0xC8ADBD64, 0xE71E, 0x48A0, [8]byte{0xA4, 0xDE, 0x18, 0x5C, 0x39, 0x5C, 0xD3, 0x17})

	subtypePCM       = mkGUID(0x00000001, 0x0000, 0x0010, [8]byte{0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71})
	subtypeIEEEFloat = mkGUID(0x00000003, 0x0000, 0x0010, [8]byte{0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71})

	pkeyDeviceFriendlyName = propertyKey{
		fmtid: mkGUID(0xA45C254E, 0xDF1C, 0x4EFD, [8]byte{0x80, 0x20, 0x67, 0xD1, 0x46, 0xA8, 0x50, 0xE0}),
		pid:   14,
	}

	// PKEY_AudioEngine_DeviceFormat: the device's physical format, used for
	// exclusive mode negotiation.
	pkeyAudioEngineDeviceFormat = propertyKey{
		fmtid: mkGUID(0xF19F064D, 0x082C, 0x4E27, [8]byte{0xBC, 0x73, 0x68, 0x82, 0xA1, 0xBB, 0x8E, 0x4C}),
		pid:   0,
	}
)

type propertyKey struct {
	fmtid guid
	pid   uint32
}

// WASAPI constants.
const (
	clsctxAll = 0x17 // CLSCTX_INPROC_SERVER | CLSCTX_INPROC_HANDLER | CLSCTX_LOCAL_SERVER | CLSCTX_REMOTE_SERVER

	eRender  = 0
	eCapture = 1
	eConsole = 0

	deviceStateActive = 0x1

	audclntShareModeShared    = 0
	audclntShareModeExclusive = 1

	audclntStreamFlagsCrossProcess      = 0x00010000
	audclntStreamFlagsLoopback          = 0x00020000
	audclntStreamFlagsEventCallback     = 0x00040000
	audclntStreamFlagsNoPersist         = 0x00080000
	audclntStreamFlagsRateAdjust        = 0x00100000
	audclntStreamFlagsSrcDefaultQuality = 0x08000000
	audclntStreamFlagsAutoConvertPCM    = 0x80000000

	audclntBufferFlagsSilent = 0x2

	waveFormatPCMTag        = 0x0001
	waveFormatIEEEFloatTag  = 0x0003
	waveFormatExtensibleTag = 0xFFFE

	sOK        = 0
	sFalse     = 1
	eNotFound  = 0x80070490
	audclntEDeviceInvalidated       = 0x88890004
	audclntEUnsupportedFormat       = 0x88890008
	audclntEDeviceInUse             = 0x8889000A
	audclntEExclusiveModeNotAllowed = 0x8889000E
	audclntEBufferSizeNotAligned    = 0x88890019
	audclntEInvalidDevicePeriod     = 0x88890020
)

// waveFormatExtensible mirrors WAVEFORMATEXTENSIBLE.
type waveFormatExtensible struct {
	FormatTag      uint16
	Channels       uint16
	SamplesPerSec  uint32
	AvgBytesPerSec uint32
	BlockAlign     uint16
	BitsPerSample  uint16
	CbSize         uint16
	Samples        uint16 // Valid bits per sample.
	ChannelMask    uint32
	SubFormat      guid
}

var (
	modOle32    = windows.NewLazySystemDLL("ole32.dll")
	modAvrt     = windows.NewLazySystemDLL("avrt.dll")

	procCoInitializeEx   = modOle32.NewProc("CoInitializeEx")
	procCoUninitialize   = modOle32.NewProc("CoUninitialize")
	procCoCreateInstance = modOle32.NewProc("CoCreateInstance")
	procCoTaskMemFree    = modOle32.NewProc("CoTaskMemFree")
	procPropVariantClear = modOle32.NewProc("PropVariantClear")

	procAvSetMmThreadCharacteristicsW   = modAvrt.NewProc("AvSetMmThreadCharacteristicsW")
	procAvRevertMmThreadCharacteristics = modAvrt.NewProc("AvRevertMmThreadCharacteristics")
	procAvSetMmThreadPriority           = modAvrt.NewProc("AvSetMmThreadPriority")
)

const coinitApartmentThreaded = 0x2
const coinitMultiThreaded = 0x0

func coInitializeEx() {
	// S_FALSE/RPC_E_CHANGED_MODE are fine; COM is already initialized.
	procCoInitializeEx.Call(0, coinitMultiThreaded)
}

func coUninitialize() {
	procCoUninitialize.Call()
}

func coTaskMemFree(p unsafe.Pointer) {
	if p != nil {
		procCoTaskMemFree.Call(uintptr(p))
	}
}

// hresultToResult converts an HRESULT to a Result.
func hresultToResult(hr uintptr) Result {
	switch uint32(hr) {
	case sOK:
		return Success
	case audclntEDeviceInvalidated:
		return ErrNoDevice
	case audclntEUnsupportedFormat:
		return ErrFormatNotSupported
	case audclntEDeviceInUse:
		return ErrAlreadyInUse
	case audclntEExclusiveModeNotAllowed:
		return ErrShareModeNotSupported
	case 0x80070005: // E_ACCESSDENIED
		return ErrAccessDenied
	case 0x8007000E: // E_OUTOFMEMORY
		return ErrOutOfMemory
	case 0x80070057: // E_INVALIDARG
		return ErrInvalidArgs
	default:
		return ErrorGeneric
	}
}

func hrFailed(hr uintptr) bool { return int32(hr) < 0 }

// comInstance matches the memory layout of a COM object: the first word is
// a pointer to the vtable.
type comInstance struct {
	vtbl *[64]uintptr
}

// comObject is a raw COM interface pointer.
type comObject = *comInstance

func (o comObject) call(index uintptr, args ...uintptr) uintptr {
	callArgs := make([]uintptr, 0, 8)
	callArgs = append(callArgs, uintptr(unsafe.Pointer(o)))
	callArgs = append(callArgs, args...)
	ret, _, _ := syscall.SyscallN(o.vtbl[index], callArgs...)
	return ret
}

// Release decrements the reference count (IUnknown::Release, slot 2).
func (o comObject) Release() {
	if o != nil {
		o.call(2)
	}
}

func coCreateInstance(clsid, iid *guid) (comObject, error) {
	var obj comObject
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(clsid)),
		0,
		clsctxAll,
		uintptr(unsafe.Pointer(iid)),
		uintptr(unsafe.Pointer(&obj)),
	)
	if hrFailed(hr) {
		return nil, hresultToResult(hr)
	}
	return obj, nil
}

/* IMMDeviceEnumerator */

func (o comObject) enumAudioEndpoints(dataFlow uint32, stateMask uint32) (comObject, error) {
	var coll comObject
	hr := o.call(3, uintptr(dataFlow), uintptr(stateMask), uintptr(unsafe.Pointer(&coll)))
	if hrFailed(hr) {
		return nil, hresultToResult(hr)
	}
	return coll, nil
}

func (o comObject) getDefaultAudioEndpoint(dataFlow uint32, role uint32) (comObject, error) {
	var dev comObject
	hr := o.call(4, uintptr(dataFlow), uintptr(role), uintptr(unsafe.Pointer(&dev)))
	if hrFailed(hr) {
		return nil, hresultToResult(hr)
	}
	return dev, nil
}

func (o comObject) getDevice(id *uint16) (comObject, error) {
	var dev comObject
	hr := o.call(5, uintptr(unsafe.Pointer(id)), uintptr(unsafe.Pointer(&dev)))
	if hrFailed(hr) {
		return nil, hresultToResult(hr)
	}
	return dev, nil
}

/* IMMDeviceCollection */

func (o comObject) getCount() (uint32, error) {
	var n uint32
	hr := o.call(3, uintptr(unsafe.Pointer(&n)))
	if hrFailed(hr) {
		return 0, hresultToResult(hr)
	}
	return n, nil
}

func (o comObject) item(i uint32) (comObject, error) {
	var dev comObject
	hr := o.call(4, uintptr(i), uintptr(unsafe.Pointer(&dev)))
	if hrFailed(hr) {
		return nil, hresultToResult(hr)
	}
	return dev, nil
}

/* IMMDevice */

func (o comObject) activate(iid *guid) (comObject, error) {
	var out comObject
	hr := o.call(3, uintptr(unsafe.Pointer(iid)), clsctxAll, 0, uintptr(unsafe.Pointer(&out)))
	if hrFailed(hr) {
		return nil, hresultToResult(hr)
	}
	return out, nil
}

func (o comObject) openPropertyStore() (comObject, error) {
	const stgmRead = 0
	var ps comObject
	hr := o.call(4, stgmRead, uintptr(unsafe.Pointer(&ps)))
	if hrFailed(hr) {
		return nil, hresultToResult(hr)
	}
	return ps, nil
}

func (o comObject) getID() (string, error) {
	var p *uint16
	hr := o.call(5, uintptr(unsafe.Pointer(&p)))
	if hrFailed(hr) {
		return "", hresultToResult(hr)
	}
	s := windows.UTF16PtrToString(p)
	coTaskMemFree(unsafe.Pointer(p))
	return s, nil
}

/* IPropertyStore */

// propVariant mirrors PROPVARIANT for the VT_LPWSTR case, where the value
// union holds a COM-allocated wide string pointer.
type propVariant struct {
	Vt  uint16
	_   [6]byte
	Val *uint16
	_   uintptr
}

const (
	vtLPWSTR = 31
	vtBlob   = 65
)

// propVariantBlob mirrors PROPVARIANT for the VT_BLOB case: the union holds
// a BLOB {ULONG cbSize; BYTE* pBlobData}.
type propVariantBlob struct {
	Vt     uint16
	_      [6]byte
	CbSize uint32
	_      uint32
	Ptr    *byte
}

// getBlobValue reads a VT_BLOB property, returning a copy of its bytes.
func (o comObject) getBlobValue(key *propertyKey) ([]byte, error) {
	var pv propVariantBlob
	hr := o.call(5, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&pv)))
	if hrFailed(hr) {
		return nil, hresultToResult(hr)
	}
	defer procPropVariantClear.Call(uintptr(unsafe.Pointer(&pv)))
	if pv.Vt != vtBlob || pv.Ptr == nil || pv.CbSize == 0 {
		return nil, ErrNoDataAvailable
	}
	out := make([]byte, pv.CbSize)
	copy(out, unsafe.Slice(pv.Ptr, pv.CbSize))
	return out, nil
}

func (o comObject) getStringValue(key *propertyKey) (string, error) {
	var pv propVariant
	hr := o.call(5, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&pv)))
	if hrFailed(hr) {
		return "", hresultToResult(hr)
	}
	defer procPropVariantClear.Call(uintptr(unsafe.Pointer(&pv)))
	if pv.Vt != vtLPWSTR || pv.Val == nil {
		return "", ErrNoDataAvailable
	}
	return windows.UTF16PtrToString(pv.Val), nil
}

/* IAudioClient / IAudioClient3 */

func (o comObject) initialize(shareMode, streamFlags uint32, bufferDuration, periodicity int64, format *waveFormatExtensible) error {
	hr := o.initializeRaw(shareMode, streamFlags, bufferDuration, periodicity, format)
	if hrFailed(hr) {
		return hresultToResult(hr)
	}
	return nil
}

// initializeRaw returns the raw HRESULT so callers can react to specific
// codes (e.g. AUDCLNT_E_BUFFER_SIZE_NOT_ALIGNED in exclusive mode).
func (o comObject) initializeRaw(shareMode, streamFlags uint32, bufferDuration, periodicity int64, format *waveFormatExtensible) uintptr {
	return o.call(3, uintptr(shareMode), uintptr(streamFlags), uintptr(bufferDuration), uintptr(periodicity), uintptr(unsafe.Pointer(format)), 0)
}

// isFormatSupported checks a format against a share mode (IAudioClient
// slot 7). In exclusive mode no closest-match is produced; any returned
// shared-mode closest match is freed.
func (o comObject) isFormatSupported(shareMode uint32, format *waveFormatExtensible) bool {
	var closest *waveFormatExtensible
	hr := o.call(7, uintptr(shareMode), uintptr(unsafe.Pointer(format)), uintptr(unsafe.Pointer(&closest)))
	if closest != nil {
		coTaskMemFree(unsafe.Pointer(closest))
	}
	return uint32(hr) == sOK
}

func (o comObject) getBufferSize() (uint32, error) {
	var n uint32
	hr := o.call(4, uintptr(unsafe.Pointer(&n)))
	if hrFailed(hr) {
		return 0, hresultToResult(hr)
	}
	return n, nil
}

func (o comObject) getStreamLatency() (int64, error) {
	var t int64
	hr := o.call(5, uintptr(unsafe.Pointer(&t)))
	if hrFailed(hr) {
		return 0, hresultToResult(hr)
	}
	return t, nil
}

func (o comObject) getCurrentPadding() (uint32, error) {
	var n uint32
	hr := o.call(6, uintptr(unsafe.Pointer(&n)))
	if hrFailed(hr) {
		return 0, hresultToResult(hr)
	}
	return n, nil
}

func (o comObject) getMixFormat() (*waveFormatExtensible, error) {
	var p *waveFormatExtensible
	hr := o.call(8, uintptr(unsafe.Pointer(&p)))
	if hrFailed(hr) {
		return nil, hresultToResult(hr)
	}
	return p, nil
}

func (o comObject) getDevicePeriod() (defaultPeriod, minPeriod int64, err error) {
	hr := o.call(9, uintptr(unsafe.Pointer(&defaultPeriod)), uintptr(unsafe.Pointer(&minPeriod)))
	if hrFailed(hr) {
		return 0, 0, hresultToResult(hr)
	}
	return defaultPeriod, minPeriod, nil
}

func (o comObject) startClient() error {
	hr := o.call(10)
	if hrFailed(hr) {
		return hresultToResult(hr)
	}
	return nil
}

func (o comObject) stopClient() error {
	hr := o.call(11)
	if hrFailed(hr) {
		return hresultToResult(hr)
	}
	return nil
}

func (o comObject) resetClient() error {
	hr := o.call(12)
	if hrFailed(hr) {
		return hresultToResult(hr)
	}
	return nil
}

func (o comObject) setEventHandle(h windows.Handle) error {
	hr := o.call(13, uintptr(h))
	if hrFailed(hr) {
		return hresultToResult(hr)
	}
	return nil
}

func (o comObject) getService(iid *guid) (comObject, error) {
	var out comObject
	hr := o.call(14, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))
	if hrFailed(hr) {
		return nil, hresultToResult(hr)
	}
	return out, nil
}

// IAudioClient3. The vtable extends IAudioClient2, so the IAudioClient2
// methods (IsOffloadCapable=15, SetClientProperties=16,
// GetBufferSizeLimits=17) occupy the slots before the IAudioClient3
// additions: GetSharedModeEnginePeriod=18,
// GetCurrentSharedModeEnginePeriod=19, InitializeSharedAudioStream=20.

func (o comObject) getSharedModeEnginePeriod(format *waveFormatExtensible) (def, fundamental, minP, maxP uint32, err error) {
	hr := o.call(18, uintptr(unsafe.Pointer(format)),
		uintptr(unsafe.Pointer(&def)), uintptr(unsafe.Pointer(&fundamental)),
		uintptr(unsafe.Pointer(&minP)), uintptr(unsafe.Pointer(&maxP)))
	if hrFailed(hr) {
		return 0, 0, 0, 0, hresultToResult(hr)
	}
	return def, fundamental, minP, maxP, nil
}

func (o comObject) initializeSharedAudioStream(streamFlags, periodInFrames uint32, format *waveFormatExtensible) error {
	hr := o.call(20, uintptr(streamFlags), uintptr(periodInFrames), uintptr(unsafe.Pointer(format)), 0)
	if hrFailed(hr) {
		return hresultToResult(hr)
	}
	return nil
}

/* IAudioRenderClient */

func (o comObject) getRenderBuffer(frames uint32) (*byte, error) {
	var p *byte
	hr := o.call(3, uintptr(frames), uintptr(unsafe.Pointer(&p)))
	if hrFailed(hr) {
		return nil, hresultToResult(hr)
	}
	return p, nil
}

func (o comObject) releaseRenderBuffer(frames, flags uint32) error {
	hr := o.call(4, uintptr(frames), uintptr(flags))
	if hrFailed(hr) {
		return hresultToResult(hr)
	}
	return nil
}

/* IAudioCaptureClient */

func (o comObject) getCaptureBuffer() (data *byte, frames uint32, flags uint32, err error) {
	var pos, qpc uint64
	hr := o.call(3, uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&frames)), uintptr(unsafe.Pointer(&flags)), uintptr(unsafe.Pointer(&pos)), uintptr(unsafe.Pointer(&qpc)))
	if uint32(hr) == 0x08890001 { // AUDCLNT_S_BUFFER_EMPTY
		return nil, 0, 0, nil
	}
	if hrFailed(hr) {
		return nil, 0, 0, hresultToResult(hr)
	}
	return data, frames, flags, nil
}

func (o comObject) releaseCaptureBuffer(frames uint32) error {
	hr := o.call(4, uintptr(frames))
	if hrFailed(hr) {
		return hresultToResult(hr)
	}
	return nil
}

func (o comObject) getNextPacketSize() (uint32, error) {
	var n uint32
	hr := o.call(5, uintptr(unsafe.Pointer(&n)))
	if hrFailed(hr) {
		return 0, hresultToResult(hr)
	}
	return n, nil
}

/* AVRT thread priority */

// avrtPriorityCritical mirrors AVRT_PRIORITY_CRITICAL.
const avrtPriorityCritical = 2

// enableProAudioThreadPriority opts the calling thread into MMCSS "Pro
// Audio" scheduling at critical priority (the default within the class is
// only NORMAL). Returns a handle for revert, or 0.
func enableProAudioThreadPriority() uintptr {
	task, _ := windows.UTF16PtrFromString("Pro Audio")
	var taskIndex uint32
	h, _, _ := procAvSetMmThreadCharacteristicsW.Call(uintptr(unsafe.Pointer(task)), uintptr(unsafe.Pointer(&taskIndex)))
	if h != 0 {
		procAvSetMmThreadPriority.Call(h, avrtPriorityCritical)
	}
	return h
}

func revertThreadPriority(h uintptr) {
	if h != 0 {
		procAvRevertMmThreadCharacteristics.Call(h)
	}
}

// wasapiFormatToNative converts a WAVEFORMATEX(TENSIBLE) to our Format.
func wasapiFormatToNative(wf *waveFormatExtensible) Format {
	tag := wf.FormatTag
	bits := wf.BitsPerSample
	if tag == waveFormatExtensibleTag {
		switch wf.SubFormat {
		case subtypeIEEEFloat:
			tag = waveFormatIEEEFloatTag
		case subtypePCM:
			tag = waveFormatPCMTag
		default:
			return FormatUnknown
		}
		// For extensible formats the container size is BitsPerSample and the
		// valid bits are in Samples. miniaudio maps by container size.
	}
	switch tag {
	case waveFormatIEEEFloatTag:
		if bits == 32 {
			return FormatF32
		}
	case waveFormatPCMTag:
		switch bits {
		case 8:
			return FormatU8
		case 16:
			return FormatS16
		case 24:
			return FormatS24
		case 32:
			return FormatS32
		}
	}
	return FormatUnknown
}

// nativeFormatToWasapi builds a WAVEFORMATEXTENSIBLE for one of our formats.
func nativeFormatToWasapi(format Format, channels, sampleRate uint32) waveFormatExtensible {
	bits := uint16(format.SizeInBytes() * 8)
	blockAlign := uint16(FrameSizeInBytes(format, channels))
	wf := waveFormatExtensible{
		FormatTag:      waveFormatExtensibleTag,
		Channels:       uint16(channels),
		SamplesPerSec:  sampleRate,
		AvgBytesPerSec: sampleRate * uint32(blockAlign),
		BlockAlign:     blockAlign,
		BitsPerSample:  bits,
		CbSize:         22,
		Samples:        bits, // Valid bits per sample.
		ChannelMask:    wasapiChannelMaskFromChannels(channels),
	}
	if format == FormatF32 {
		wf.SubFormat = subtypeIEEEFloat
	} else {
		wf.SubFormat = subtypePCM
	}
	return wf
}

// wasapiChannelMaskFromChannels returns a standard speaker mask for a
// channel count, mirroring ma_channel_map_to_channel_mask__win32 defaults.
func wasapiChannelMaskFromChannels(channels uint32) uint32 {
	switch channels {
	case 1:
		return 0x4 // FRONT_CENTER
	case 2:
		return 0x3 // FRONT_LEFT | FRONT_RIGHT
	case 4:
		return 0x33 // FL | FR | BL | BR
	case 6:
		return 0x3F // FL | FR | FC | LFE | BL | BR
	case 8:
		return 0x63F // 7.1
	default:
		// Set the low bits; WASAPI accepts a best-effort mask.
		var mask uint32
		for i := uint32(0); i < channels && i < 18; i++ {
			mask |= 1 << i
		}
		return mask
	}
}

// parseWaveFormatBlob parses a WAVEFORMATEX(TENSIBLE) property blob (e.g.
// PKEY_AudioEngine_DeviceFormat).
func parseWaveFormatBlob(blob []byte) (waveFormatExtensible, bool) {
	var wf waveFormatExtensible
	if len(blob) < 16 {
		return wf, false
	}
	if len(blob) >= 40 {
		wf = *(*waveFormatExtensible)(unsafe.Pointer(&blob[0]))
		return wf, true
	}
	// Plain WAVEFORMATEX.
	wf.FormatTag = uint16(blob[0]) | uint16(blob[1])<<8
	wf.Channels = uint16(blob[2]) | uint16(blob[3])<<8
	wf.SamplesPerSec = loadU32(blob, 4)
	wf.AvgBytesPerSec = loadU32(blob, 8)
	wf.BlockAlign = uint16(blob[12]) | uint16(blob[13])<<8
	wf.BitsPerSample = uint16(blob[14]) | uint16(blob[15])<<8
	return wf, true
}

// wasapiChannelMaskToChannelMap converts a WASAPI dwChannelMask to a channel
// map, mirroring ma_channel_mask_to_channel_map__win32.
func wasapiChannelMaskToChannelMap(mask uint32, channels uint32) []Channel {
	if mask == 0 || channels == 0 {
		return nil
	}
	// WASAPI speaker bits map 1:1 onto our channel positions starting at
	// FRONT_LEFT.
	channelMap := make([]Channel, 0, channels)
	for bit := 0; bit < 18 && uint32(len(channelMap)) < channels; bit++ {
		if mask&(1<<bit) != 0 {
			channelMap = append(channelMap, ChannelFrontLeft+Channel(bit))
		}
	}
	for uint32(len(channelMap)) < channels {
		channelMap = append(channelMap, ChannelNone)
	}
	return channelMap
}
