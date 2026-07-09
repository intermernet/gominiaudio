package gominiaudio

// Version of the miniaudio API this library replicates.
const (
	VersionMajor    = 0
	VersionMinor    = 11
	VersionRevision = 21
	VersionString   = "0.11.21"
)

// Format mirrors ma_format. Sample formats are always native-endian
// interleaved unless stated otherwise.
type Format uint32

const (
	FormatUnknown Format = 0 // Mostly used for indicating an error, but also used as the default for the output format for decoders.
	FormatU8      Format = 1
	FormatS16     Format = 2 // Seems to be the most widely supported format.
	FormatS24     Format = 3 // Tightly packed. 3 bytes per sample.
	FormatS32     Format = 4
	FormatF32     Format = 5
	FormatCount          = 6
)

// SizeInBytes mirrors ma_get_bytes_per_sample.
func (f Format) SizeInBytes() int {
	switch f {
	case FormatU8:
		return 1
	case FormatS16:
		return 2
	case FormatS24:
		return 3
	case FormatS32, FormatF32:
		return 4
	default:
		return 0
	}
}

// String returns a human readable name for the format.
func (f Format) String() string {
	switch f {
	case FormatUnknown:
		return "Unknown"
	case FormatU8:
		return "8-bit Unsigned Integer"
	case FormatS16:
		return "16-bit Signed Integer"
	case FormatS24:
		return "24-bit Signed Integer (Tightly Packed)"
	case FormatS32:
		return "32-bit Signed Integer"
	case FormatF32:
		return "32-bit IEEE Floating Point"
	default:
		return "Invalid"
	}
}

// FrameSizeInBytes mirrors ma_get_bytes_per_frame.
func FrameSizeInBytes(format Format, channels uint32) int {
	return format.SizeInBytes() * int(channels)
}

// Common limits, mirroring miniaudio's defines.
const (
	MaxChannels = 254

	MinSampleRate = 8000
	MaxSampleRate = 384000

	MaxDeviceNameLength = 255
	MaxDeviceIDLength   = 256
	MaxLogLevels        = 4
)

// StandardSampleRates mirrors ma_standard_sample_rate_* sorted by priority.
var StandardSampleRates = [...]uint32{
	48000, 44100,
	32000, 24000, 22050,
	88200, 96000, 176400, 192000,
	16000, 11025, 8000,
	352800, 384000,
}

// SampleRate constants mirroring ma_standard_sample_rate_*.
const (
	SampleRate8000   = 8000
	SampleRate11025  = 11025
	SampleRate16000  = 16000
	SampleRate22050  = 22050
	SampleRate24000  = 24000
	SampleRate32000  = 32000
	SampleRate44100  = 44100
	SampleRate48000  = 48000
	SampleRate88200  = 88200
	SampleRate96000  = 96000
	SampleRate176400 = 176400
	SampleRate192000 = 192000
	SampleRate352800 = 352800
	SampleRate384000 = 384000

	SampleRateMin = SampleRate8000
	SampleRateMax = SampleRate384000
)

// Channel mirrors ma_channel: a channel position.
type Channel uint8

// Channel positions, mirroring MA_CHANNEL_*.
const (
	ChannelNone           Channel = 0
	ChannelMono           Channel = 1
	ChannelFrontLeft      Channel = 2
	ChannelFrontRight     Channel = 3
	ChannelFrontCenter    Channel = 4
	ChannelLFE            Channel = 5
	ChannelBackLeft       Channel = 6
	ChannelBackRight      Channel = 7
	ChannelFrontLeftCenter  Channel = 8
	ChannelFrontRightCenter Channel = 9
	ChannelBackCenter     Channel = 10
	ChannelSideLeft       Channel = 11
	ChannelSideRight      Channel = 12
	ChannelTopCenter      Channel = 13
	ChannelTopFrontLeft   Channel = 14
	ChannelTopFrontCenter Channel = 15
	ChannelTopFrontRight  Channel = 16
	ChannelTopBackLeft    Channel = 17
	ChannelTopBackCenter  Channel = 18
	ChannelTopBackRight   Channel = 19
	ChannelAux0           Channel = 20
	ChannelAux31          Channel = 51

	ChannelLeft  = ChannelFrontLeft
	ChannelRight = ChannelFrontRight

	ChannelPositionCount = ChannelAux0 + 32
)

// ChannelAux returns the nth auxiliary channel position (0..31).
func ChannelAux(n int) Channel {
	if n < 0 || n > 31 {
		return ChannelNone
	}
	return ChannelAux0 + Channel(n)
}

// StandardChannelMap mirrors ma_standard_channel_map.
type StandardChannelMap uint32

const (
	StandardChannelMapMicrosoft StandardChannelMap = 0
	StandardChannelMapALSA      StandardChannelMap = 1
	StandardChannelMapRFC3551   StandardChannelMap = 2 // Based on AIFF.
	StandardChannelMapFLAC      StandardChannelMap = 3
	StandardChannelMapVorbis    StandardChannelMap = 4
	StandardChannelMapSound4    StandardChannelMap = 5 // FreeBSD's sound(4).
	StandardChannelMapSNDIO     StandardChannelMap = 6 // sndio channel map.
	StandardChannelMapWebAudio                     = StandardChannelMapFLAC

	StandardChannelMapDefault = StandardChannelMapMicrosoft
)

// DitherMode mirrors ma_dither_mode.
type DitherMode uint32

const (
	DitherModeNone      DitherMode = 0
	DitherModeRectangle DitherMode = 1
	DitherModeTriangle  DitherMode = 2
)

// DeviceType mirrors ma_device_type.
type DeviceType uint32

const (
	DeviceTypePlayback DeviceType = 1
	DeviceTypeCapture  DeviceType = 2
	DeviceTypeDuplex   DeviceType = DeviceTypePlayback | DeviceTypeCapture
	DeviceTypeLoopback DeviceType = 4
)

// ShareMode mirrors ma_share_mode.
type ShareMode uint32

const (
	ShareModeShared    ShareMode = 0
	ShareModeExclusive ShareMode = 1
)

// PerformanceProfile mirrors ma_performance_profile.
type PerformanceProfile uint32

const (
	PerformanceProfileLowLatency   PerformanceProfile = 0
	PerformanceProfileConservative PerformanceProfile = 1
)

// Backend mirrors ma_backend, restricted to the backends this library
// implements plus Null.
type Backend uint32

const (
	BackendWASAPI    Backend = 0
	BackendCoreAudio Backend = 1
	BackendPipeWire  Backend = 2
	BackendNull      Backend = 3
	BackendCount             = 4
)

// String returns the backend name, mirroring ma_get_backend_name.
func (b Backend) String() string {
	switch b {
	case BackendWASAPI:
		return "WASAPI"
	case BackendCoreAudio:
		return "Core Audio"
	case BackendPipeWire:
		return "PipeWire"
	case BackendNull:
		return "Null"
	default:
		return "Unknown"
	}
}

// DeviceState mirrors ma_device_state.
type DeviceState uint32

const (
	DeviceStateUninitialized DeviceState = 0
	DeviceStateStopped       DeviceState = 1 // The device's default state after initialization.
	DeviceStateStarted       DeviceState = 2 // The device is started and is requesting and/or delivering audio data.
	DeviceStateStarting      DeviceState = 3 // Transitioning from a stopped state to started.
	DeviceStateStopping      DeviceState = 4 // Transitioning from a started state to stopped.
)

// PanMode mirrors ma_pan_mode.
type PanMode uint32

const (
	PanModeBalance PanMode = 0 // Does not blend one side with the other. Technically just a balance. Compatible with other popular audio engines and therefore the default.
	PanModePan     PanMode = 1 // A true pan. The sound from one side will "move" to the other side and blend with it.
)

// Positioning mirrors ma_positioning.
type Positioning uint32

const (
	PositioningAbsolute Positioning = 0
	PositioningRelative Positioning = 1
)

// AttenuationModel mirrors ma_attenuation_model.
type AttenuationModel uint32

const (
	AttenuationModelNone        AttenuationModel = 0 // No distance attenuation and no spatialization.
	AttenuationModelInverse     AttenuationModel = 1 // Equivalent to OpenAL's AL_INVERSE_DISTANCE_CLAMPED.
	AttenuationModelLinear      AttenuationModel = 2 // Linear attenuation. Equivalent to OpenAL's AL_LINEAR_DISTANCE_CLAMPED.
	AttenuationModelExponential AttenuationModel = 3 // Exponential attenuation. Equivalent to OpenAL's AL_EXPONENT_DISTANCE_CLAMPED.
)
