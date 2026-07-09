package gominiaudio

// AudioBufferRef mirrors ma_audio_buffer_ref: a DataSource over external
// PCM memory that the caller owns. The data is not copied.
type AudioBufferRef struct {
	format     Format
	channels   uint32
	sampleRate uint32
	cursor     uint64
	sizeInFrames uint64
	data       []byte
}

// NewAudioBufferRef mirrors ma_audio_buffer_ref_init. data holds
// sizeInFrames interleaved frames and is referenced, not copied.
func NewAudioBufferRef(format Format, channels uint32, data []byte, sizeInFrames uint64) (*AudioBufferRef, error) {
	if channels == 0 || format == FormatUnknown {
		return nil, ErrInvalidArgs
	}
	return &AudioBufferRef{
		format:       format,
		channels:     channels,
		sizeInFrames: sizeInFrames,
		data:         data,
	}, nil
}

// SetData mirrors ma_audio_buffer_ref_set_data.
func (b *AudioBufferRef) SetData(data []byte, sizeInFrames uint64) error {
	b.data = data
	b.sizeInFrames = sizeInFrames
	b.cursor = 0
	return nil
}

// SetSampleRate sets the advertised sample rate (0 = unspecified).
func (b *AudioBufferRef) SetSampleRate(sampleRate uint32) { b.sampleRate = sampleRate }

// Read implements DataSource, mirroring ma_audio_buffer_ref_read_pcm_frames.
func (b *AudioBufferRef) Read(framesOut []byte, frameCount uint64) (uint64, error) {
	if b.cursor >= b.sizeInFrames {
		return 0, ErrAtEnd
	}
	avail := b.sizeInFrames - b.cursor
	n := min(frameCount, avail)
	bpf := uint64(FrameSizeInBytes(b.format, b.channels))
	if framesOut != nil {
		copy(framesOut[:n*bpf], b.data[b.cursor*bpf:(b.cursor+n)*bpf])
	}
	b.cursor += n
	if n < frameCount {
		return n, ErrAtEnd
	}
	return n, nil
}

// Seek implements DataSource.
func (b *AudioBufferRef) Seek(frameIndex uint64) error {
	if frameIndex > b.sizeInFrames {
		return ErrInvalidArgs
	}
	b.cursor = frameIndex
	return nil
}

// DataFormat implements DataSource.
func (b *AudioBufferRef) DataFormat() (Format, uint32, uint32, []Channel, error) {
	return b.format, b.channels, b.sampleRate, nil, nil
}

// Cursor implements DataSource.
func (b *AudioBufferRef) Cursor() (uint64, error) { return b.cursor, nil }

// Length implements DataSource.
func (b *AudioBufferRef) Length() (uint64, error) { return b.sizeInFrames, nil }

// AvailableFrames mirrors ma_audio_buffer_ref_get_available_frames.
func (b *AudioBufferRef) AvailableFrames() uint64 {
	if b.cursor >= b.sizeInFrames {
		return 0
	}
	return b.sizeInFrames - b.cursor
}

// AtEnd mirrors ma_audio_buffer_ref_at_end.
func (b *AudioBufferRef) AtEnd() bool { return b.cursor >= b.sizeInFrames }

// AudioBufferConfig mirrors ma_audio_buffer_config.
type AudioBufferConfig struct {
	Format       Format
	Channels     uint32
	SampleRate   uint32
	SizeInFrames uint64
	Data         []byte // Initial data to copy in. May be nil for a zeroed buffer.
}

// AudioBufferConfigInit mirrors ma_audio_buffer_config_init.
func AudioBufferConfigInit(format Format, channels uint32, sizeInFrames uint64, data []byte) AudioBufferConfig {
	return AudioBufferConfig{Format: format, Channels: channels, SizeInFrames: sizeInFrames, Data: data}
}

// AudioBuffer mirrors ma_audio_buffer: like AudioBufferRef but owns a copy
// of the data.
type AudioBuffer struct {
	AudioBufferRef
}

// NewAudioBuffer mirrors ma_audio_buffer_init (with copy semantics, i.e.
// ma_audio_buffer_init_copy when Data is provided).
func NewAudioBuffer(config AudioBufferConfig) (*AudioBuffer, error) {
	if config.Channels == 0 || config.Format == FormatUnknown || config.SizeInFrames == 0 {
		return nil, ErrInvalidArgs
	}
	bpf := uint64(FrameSizeInBytes(config.Format, config.Channels))
	data := make([]byte, config.SizeInFrames*bpf)
	if config.Data != nil {
		copy(data, config.Data)
	}
	buf := &AudioBuffer{}
	buf.format = config.Format
	buf.channels = config.Channels
	buf.sampleRate = config.SampleRate
	buf.sizeInFrames = config.SizeInFrames
	buf.data = data
	return buf, nil
}
