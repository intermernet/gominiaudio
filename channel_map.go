package gominiaudio

// ChannelMapInitStandard mirrors ma_channel_map_init_standard. It returns a
// channel map for the given standard layout and channel count.
func ChannelMapInitStandard(standard StandardChannelMap, channels uint32) []Channel {
	m := make([]Channel, channels)
	for i := uint32(0); i < channels; i++ {
		m[i] = standardChannelMapPosition(standard, channels, i)
	}
	return m
}

func standardChannelMapPosition(standard StandardChannelMap, channelCount, channelIndex uint32) Channel {
	var m []Channel
	switch standard {
	case StandardChannelMapALSA:
		m = alsaMap(channelCount)
	case StandardChannelMapRFC3551:
		m = rfc3551Map(channelCount)
	case StandardChannelMapFLAC:
		m = flacMap(channelCount)
	case StandardChannelMapVorbis:
		m = vorbisMap(channelCount)
	case StandardChannelMapSound4:
		m = sound4Map(channelCount)
	case StandardChannelMapSNDIO:
		m = sndioMap(channelCount)
	default: // Microsoft
		m = microsoftMap(channelCount)
	}
	if int(channelIndex) < len(m) {
		return m[channelIndex]
	}
	// Beyond the standard positions, assign aux channels.
	aux := int(channelIndex) - len(m)
	if aux <= 31 {
		return ChannelAux0 + Channel(aux)
	}
	return ChannelNone
}

func microsoftMap(n uint32) []Channel {
	switch n {
	case 0:
		return nil
	case 1:
		return []Channel{ChannelMono}
	case 2:
		return []Channel{ChannelFrontLeft, ChannelFrontRight}
	case 3:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelFrontCenter}
	case 4:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelBackLeft, ChannelBackRight}
	case 5:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelFrontCenter, ChannelBackLeft, ChannelBackRight}
	case 6:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelFrontCenter, ChannelLFE, ChannelBackLeft, ChannelBackRight}
	case 7:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelFrontCenter, ChannelLFE, ChannelBackCenter, ChannelSideLeft, ChannelSideRight}
	default:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelFrontCenter, ChannelLFE, ChannelBackLeft, ChannelBackRight, ChannelSideLeft, ChannelSideRight}
	}
}

func alsaMap(n uint32) []Channel {
	switch n {
	case 0:
		return nil
	case 1:
		return []Channel{ChannelMono}
	case 2:
		return []Channel{ChannelFrontLeft, ChannelFrontRight}
	case 3:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelFrontCenter}
	case 4:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelBackLeft, ChannelBackRight}
	case 5:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelBackLeft, ChannelBackRight, ChannelFrontCenter}
	case 6:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelBackLeft, ChannelBackRight, ChannelFrontCenter, ChannelLFE}
	case 7:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelBackLeft, ChannelBackRight, ChannelFrontCenter, ChannelLFE, ChannelBackCenter}
	default:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelBackLeft, ChannelBackRight, ChannelFrontCenter, ChannelLFE, ChannelSideLeft, ChannelSideRight}
	}
}

func rfc3551Map(n uint32) []Channel {
	switch n {
	case 0:
		return nil
	case 1:
		return []Channel{ChannelMono}
	case 2:
		return []Channel{ChannelFrontLeft, ChannelFrontRight}
	case 3:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelFrontCenter}
	case 4:
		return []Channel{ChannelFrontLeft, ChannelFrontCenter, ChannelFrontRight, ChannelBackCenter}
	case 5:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelFrontCenter, ChannelBackLeft, ChannelBackRight}
	default:
		return []Channel{ChannelFrontLeft, ChannelFrontLeftCenter, ChannelFrontCenter, ChannelFrontRight, ChannelFrontRightCenter, ChannelBackCenter}
	}
}

func flacMap(n uint32) []Channel {
	switch n {
	case 0:
		return nil
	case 1:
		return []Channel{ChannelMono}
	case 2:
		return []Channel{ChannelFrontLeft, ChannelFrontRight}
	case 3:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelFrontCenter}
	case 4:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelBackLeft, ChannelBackRight}
	case 5:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelFrontCenter, ChannelBackLeft, ChannelBackRight}
	case 6:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelFrontCenter, ChannelLFE, ChannelBackLeft, ChannelBackRight}
	case 7:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelFrontCenter, ChannelLFE, ChannelBackCenter, ChannelSideLeft, ChannelSideRight}
	default:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelFrontCenter, ChannelLFE, ChannelBackLeft, ChannelBackRight, ChannelSideLeft, ChannelSideRight}
	}
}

func vorbisMap(n uint32) []Channel {
	switch n {
	case 0:
		return nil
	case 1:
		return []Channel{ChannelMono}
	case 2:
		return []Channel{ChannelFrontLeft, ChannelFrontRight}
	case 3:
		return []Channel{ChannelFrontLeft, ChannelFrontCenter, ChannelFrontRight}
	case 4:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelBackLeft, ChannelBackRight}
	case 5:
		return []Channel{ChannelFrontLeft, ChannelFrontCenter, ChannelFrontRight, ChannelBackLeft, ChannelBackRight}
	case 6:
		return []Channel{ChannelFrontLeft, ChannelFrontCenter, ChannelFrontRight, ChannelBackLeft, ChannelBackRight, ChannelLFE}
	case 7:
		return []Channel{ChannelFrontLeft, ChannelFrontCenter, ChannelFrontRight, ChannelSideLeft, ChannelSideRight, ChannelBackCenter, ChannelLFE}
	default:
		return []Channel{ChannelFrontLeft, ChannelFrontCenter, ChannelFrontRight, ChannelSideLeft, ChannelSideRight, ChannelBackLeft, ChannelBackRight, ChannelLFE}
	}
}

func sound4Map(n uint32) []Channel {
	switch n {
	case 0:
		return nil
	case 1:
		return []Channel{ChannelMono}
	case 2:
		return []Channel{ChannelFrontLeft, ChannelFrontRight}
	case 3:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelFrontCenter}
	case 4:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelBackLeft, ChannelBackRight}
	case 5:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelFrontCenter, ChannelBackLeft, ChannelBackRight}
	case 6:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelFrontCenter, ChannelLFE, ChannelBackLeft, ChannelBackRight}
	case 7:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelFrontCenter, ChannelLFE, ChannelBackCenter, ChannelBackLeft, ChannelBackRight}
	default:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelFrontCenter, ChannelLFE, ChannelBackLeft, ChannelBackRight, ChannelSideLeft, ChannelSideRight}
	}
}

func sndioMap(n uint32) []Channel {
	switch n {
	case 0:
		return nil
	case 1:
		return []Channel{ChannelMono}
	case 2:
		return []Channel{ChannelFrontLeft, ChannelFrontRight}
	case 3:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelFrontCenter}
	case 4:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelBackLeft, ChannelBackRight}
	case 5:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelBackLeft, ChannelBackRight, ChannelFrontCenter}
	default:
		return []Channel{ChannelFrontLeft, ChannelFrontRight, ChannelBackLeft, ChannelBackRight, ChannelFrontCenter, ChannelLFE}
	}
}

// ChannelMapGetChannel mirrors ma_channel_map_get_channel. A nil map implies
// the default (Microsoft) standard layout.
func ChannelMapGetChannel(channelMap []Channel, channelCount, channelIndex uint32) Channel {
	if channelMap == nil {
		return standardChannelMapPosition(StandardChannelMapDefault, channelCount, channelIndex)
	}
	if channelIndex >= channelCount || int(channelIndex) >= len(channelMap) {
		return ChannelNone
	}
	return channelMap[channelIndex]
}

// ChannelMapIsValid mirrors ma_channel_map_is_valid. A nil map is valid (it
// implies the default layout). A map with a MONO channel must have one
// channel only... actually miniaudio only requires MONO maps to be
// single-channel when the MONO position appears.
func ChannelMapIsValid(channelMap []Channel, channels uint32) bool {
	if channelMap == nil {
		return true // A nil channel map is valid: default positions are assumed.
	}
	if channels == 0 || int(channels) > len(channelMap) {
		return false
	}
	// A mono map must only contain a single channel.
	if channels > 1 {
		for i := uint32(0); i < channels; i++ {
			if channelMap[i] == ChannelMono {
				return false
			}
		}
	}
	return true
}

// ChannelMapIsEqual mirrors ma_channel_map_is_equal. nil maps compare using
// default positions.
func ChannelMapIsEqual(a, b []Channel, channels uint32) bool {
	for i := uint32(0); i < channels; i++ {
		if ChannelMapGetChannel(a, channels, i) != ChannelMapGetChannel(b, channels, i) {
			return false
		}
	}
	return true
}

// ChannelMapIsBlank mirrors ma_channel_map_is_blank: every position is NONE.
func ChannelMapIsBlank(channelMap []Channel, channels uint32) bool {
	if channelMap == nil {
		return false
	}
	for i := uint32(0); i < channels && int(i) < len(channelMap); i++ {
		if channelMap[i] != ChannelNone {
			return false
		}
	}
	return true
}

// ChannelMapContainsChannelPosition mirrors
// ma_channel_map_contains_channel_position.
func ChannelMapContainsChannelPosition(channels uint32, channelMap []Channel, position Channel) bool {
	return ChannelMapFindChannelPosition(channels, channelMap, position) >= 0
}

// ChannelMapFindChannelPosition mirrors ma_channel_map_find_channel_position.
// It returns the index of the position, or -1 if not present.
func ChannelMapFindChannelPosition(channels uint32, channelMap []Channel, position Channel) int {
	for i := uint32(0); i < channels; i++ {
		if ChannelMapGetChannel(channelMap, channels, i) == position {
			return int(i)
		}
	}
	return -1
}

// ChannelPositionString returns a human readable name for a channel
// position, mirroring ma_channel_position_to_string.
func ChannelPositionString(p Channel) string {
	switch p {
	case ChannelNone:
		return "CHANNEL_NONE"
	case ChannelMono:
		return "CHANNEL_MONO"
	case ChannelFrontLeft:
		return "CHANNEL_FRONT_LEFT"
	case ChannelFrontRight:
		return "CHANNEL_FRONT_RIGHT"
	case ChannelFrontCenter:
		return "CHANNEL_FRONT_CENTER"
	case ChannelLFE:
		return "CHANNEL_LFE"
	case ChannelBackLeft:
		return "CHANNEL_BACK_LEFT"
	case ChannelBackRight:
		return "CHANNEL_BACK_RIGHT"
	case ChannelFrontLeftCenter:
		return "CHANNEL_FRONT_LEFT_CENTER"
	case ChannelFrontRightCenter:
		return "CHANNEL_FRONT_RIGHT_CENTER"
	case ChannelBackCenter:
		return "CHANNEL_BACK_CENTER"
	case ChannelSideLeft:
		return "CHANNEL_SIDE_LEFT"
	case ChannelSideRight:
		return "CHANNEL_SIDE_RIGHT"
	case ChannelTopCenter:
		return "CHANNEL_TOP_CENTER"
	case ChannelTopFrontLeft:
		return "CHANNEL_TOP_FRONT_LEFT"
	case ChannelTopFrontCenter:
		return "CHANNEL_TOP_FRONT_CENTER"
	case ChannelTopFrontRight:
		return "CHANNEL_TOP_FRONT_RIGHT"
	case ChannelTopBackLeft:
		return "CHANNEL_TOP_BACK_LEFT"
	case ChannelTopBackCenter:
		return "CHANNEL_TOP_BACK_CENTER"
	case ChannelTopBackRight:
		return "CHANNEL_TOP_BACK_RIGHT"
	}
	if p >= ChannelAux0 && p <= ChannelAux31 {
		return "CHANNEL_AUX"
	}
	return "UNKNOWN"
}
