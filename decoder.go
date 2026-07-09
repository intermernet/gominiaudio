package gominiaudio

import (
	"bytes"
	"io"
	"os"
	"strings"
)

// EncodingFormat mirrors ma_encoding_format.
type EncodingFormat uint32

const (
	EncodingFormatUnknown EncodingFormat = 0
	EncodingFormatWAV     EncodingFormat = 1
	EncodingFormatFLAC    EncodingFormat = 2
	EncodingFormatMP3     EncodingFormat = 3
	EncodingFormatVorbis  EncodingFormat = 4
)

// DecodingBackend mirrors ma_decoding_backend_vtable: a factory that
// initializes a DataSource from a stream. Custom backends (e.g. FLAC, MP3
// decoders) can be plugged into DecoderConfig.CustomBackends.
type DecodingBackend interface {
	// Init attempts to open the stream. The reader is positioned at the
	// start. Return ErrInvalidFile if the stream is not in this backend's
	// format.
	Init(r io.ReadSeeker, config *DecoderConfig) (DataSource, error)
}

// wavDecodingBackend is the built-in WAV backend.
type wavDecodingBackend struct{}

func (wavDecodingBackend) Init(r io.ReadSeeker, _ *DecoderConfig) (DataSource, error) {
	return NewWAVDecoder(r)
}

// DecoderConfig mirrors ma_decoder_config. Zero values for Format, Channels
// and SampleRate mean "use the stream's native value".
type DecoderConfig struct {
	Format         Format
	Channels       uint32
	SampleRate     uint32
	ChannelMap     []Channel
	ChannelMixMode ChannelMixMode
	DitherMode     DitherMode
	Resampling     struct {
		Algorithm ResampleAlgorithm
		Linear    struct {
			LPFOrder uint32
		}
	}
	EncodingFormat EncodingFormat
	CustomBackends []DecodingBackend
}

// DecoderConfigInit mirrors ma_decoder_config_init.
func DecoderConfigInit(outputFormat Format, outputChannels, outputSampleRate uint32) DecoderConfig {
	c := DecoderConfig{
		Format:     outputFormat,
		Channels:   outputChannels,
		SampleRate: outputSampleRate,
	}
	c.Resampling.Algorithm = ResampleAlgorithmLinear
	c.Resampling.Linear.LPFOrder = min(DefaultResamplerLPFOrder, MaxFilterOrder)
	return c
}

// DecoderConfigInitDefault mirrors ma_decoder_config_init_default.
func DecoderConfigInitDefault() DecoderConfig { return DecoderConfigInit(FormatUnknown, 0, 0) }

// Decoder mirrors ma_decoder: wraps a decoding backend and converts its
// output to the requested format/channels/rate. Decoder implements
// DataSource in its output format.
type Decoder struct {
	backend DataSource

	outputFormat     Format
	outputChannels   uint32
	outputSampleRate uint32

	nativeFormat     Format
	nativeChannels   uint32
	nativeSampleRate uint32

	converter *DataConverter // nil when the output matches the native format.
	inScratch []byte         // Native-format staging buffer for conversion.

	readCursor uint64 // Cursor in output frames.

	closer io.Closer // Optional owned resource (e.g. the file from NewDecoderFile).
}

// NewDecoder mirrors ma_decoder_init: decodes from an io.ReadSeeker.
func NewDecoder(r io.ReadSeeker, config *DecoderConfig) (*Decoder, error) {
	cfg := DecoderConfigInitDefault()
	if config != nil {
		cfg = *config
	}

	backend, err := openDecodingBackend(r, &cfg)
	if err != nil {
		return nil, err
	}
	return newDecoderFromBackend(backend, &cfg)
}

// NewDecoderMemory mirrors ma_decoder_init_memory.
func NewDecoderMemory(data []byte, config *DecoderConfig) (*Decoder, error) {
	return NewDecoder(bytes.NewReader(data), config)
}

// NewDecoderFile mirrors ma_decoder_init_file. The file remains open for the
// lifetime of the decoder; call Close to release it.
func NewDecoderFile(path string, config *DecoderConfig) (*Decoder, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrDoesNotExist
	}

	cfg := DecoderConfigInitDefault()
	if config != nil {
		cfg = *config
	}
	if cfg.EncodingFormat == EncodingFormatUnknown {
		// Hint from the file extension.
		switch {
		case strings.HasSuffix(strings.ToLower(path), ".wav"):
			cfg.EncodingFormat = EncodingFormatWAV
		}
	}

	d, derr := NewDecoder(f, &cfg)
	if derr != nil {
		f.Close()
		return nil, derr
	}
	d.closer = f
	return d, nil
}

func openDecodingBackend(r io.ReadSeeker, cfg *DecoderConfig) (DataSource, error) {
	backends := make([]DecodingBackend, 0, len(cfg.CustomBackends)+1)
	backends = append(backends, cfg.CustomBackends...)
	backends = append(backends, wavDecodingBackend{})

	for _, b := range backends {
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return nil, ErrBadSeek
		}
		ds, err := b.Init(r, cfg)
		if err == nil {
			return ds, nil
		}
	}
	return nil, ErrInvalidFile
}

func newDecoderFromBackend(backend DataSource, cfg *DecoderConfig) (*Decoder, error) {
	nativeFormat, nativeChannels, nativeRate, _, err := backend.DataFormat()
	if err != nil {
		return nil, err
	}

	d := &Decoder{
		backend:          backend,
		nativeFormat:     nativeFormat,
		nativeChannels:   nativeChannels,
		nativeSampleRate: nativeRate,
		outputFormat:     cfg.Format,
		outputChannels:   cfg.Channels,
		outputSampleRate: cfg.SampleRate,
	}
	if d.outputFormat == FormatUnknown {
		d.outputFormat = nativeFormat
	}
	if d.outputChannels == 0 {
		d.outputChannels = nativeChannels
	}
	if d.outputSampleRate == 0 {
		d.outputSampleRate = nativeRate
	}

	if d.outputFormat != nativeFormat || d.outputChannels != nativeChannels || d.outputSampleRate != nativeRate {
		dcCfg := DataConverterConfigInit(nativeFormat, d.outputFormat, nativeChannels, d.outputChannels, nativeRate, d.outputSampleRate)
		dcCfg.ChannelMapOut = cfg.ChannelMap
		dcCfg.ChannelMixMode = cfg.ChannelMixMode
		dcCfg.DitherMode = cfg.DitherMode
		dcCfg.Resampling.Algorithm = cfg.Resampling.Algorithm
		dcCfg.Resampling.Linear.LPFOrder = cfg.Resampling.Linear.LPFOrder
		conv, err := NewDataConverter(dcCfg)
		if err != nil {
			return nil, err
		}
		d.converter = conv
	}
	return d, nil
}

// Close mirrors ma_decoder_uninit: releases any owned resources.
func (d *Decoder) Close() error {
	if d.closer != nil {
		err := d.closer.Close()
		d.closer = nil
		return err
	}
	return nil
}

// Read mirrors ma_decoder_read_pcm_frames: reads frames in the output
// format. Returns ErrAtEnd alongside any final frames when exhausted.
func (d *Decoder) Read(framesOut []byte, frameCount uint64) (uint64, error) {
	if d.converter == nil {
		n, err := d.backend.Read(framesOut, frameCount)
		d.readCursor += n
		return n, err
	}

	outBpf := uint64(FrameSizeInBytes(d.outputFormat, d.outputChannels))
	inBpf := uint64(FrameSizeInBytes(d.nativeFormat, d.nativeChannels))

	var totalOut uint64
	atEnd := false
	for totalOut < frameCount && !atEnd {
		remainingOut := frameCount - totalOut
		if remainingOut > 4096 {
			remainingOut = 4096
		}
		requiredIn := d.converter.RequiredInputFrameCount(remainingOut)
		if requiredIn == 0 {
			requiredIn = 1
		}

		if uint64(cap(d.inScratch)) < requiredIn*inBpf {
			d.inScratch = make([]byte, requiredIn*inBpf)
		}
		in := d.inScratch[:requiredIn*inBpf]

		framesIn, err := d.backend.Read(in, requiredIn)
		if err == ErrAtEnd {
			atEnd = true
		} else if err != nil {
			return totalOut, err
		}
		if framesIn == 0 && atEnd {
			break
		}

		var dst []byte
		if framesOut != nil {
			dst = framesOut[totalOut*outBpf:]
		} else {
			// Seeking forward: convert into a throwaway buffer.
			if uint64(cap(d.inScratch)) < remainingOut*outBpf {
				d.inScratch = make([]byte, remainingOut*outBpf)
			}
			dst = d.inScratch[:remainingOut*outBpf]
		}

		_, produced, err := d.converter.Process(dst, in[:framesIn*inBpf], framesIn, remainingOut)
		if err != nil {
			return totalOut, err
		}
		totalOut += produced
		if produced == 0 && atEnd {
			break
		}
	}

	d.readCursor += totalOut
	if atEnd && totalOut < frameCount {
		return totalOut, ErrAtEnd
	}
	return totalOut, nil
}

// Seek mirrors ma_decoder_seek_to_pcm_frame. The frame index is in output
// frames.
func (d *Decoder) Seek(frameIndex uint64) error {
	var nativeIndex uint64
	if d.outputSampleRate == d.nativeSampleRate {
		nativeIndex = frameIndex
	} else {
		nativeIndex = frameIndex * uint64(d.nativeSampleRate) / uint64(d.outputSampleRate)
	}
	if err := d.backend.Seek(nativeIndex); err != nil {
		return err
	}
	if d.converter != nil {
		d.converter.Reset()
	}
	d.readCursor = frameIndex
	return nil
}

// DataFormat implements DataSource for the decoder's output format.
func (d *Decoder) DataFormat() (Format, uint32, uint32, []Channel, error) {
	return d.outputFormat, d.outputChannels, d.outputSampleRate, nil, nil
}

// Cursor mirrors ma_decoder_get_cursor_in_pcm_frames (output frames).
func (d *Decoder) Cursor() (uint64, error) { return d.readCursor, nil }

// Length mirrors ma_decoder_get_length_in_pcm_frames, converted to the
// output sample rate.
func (d *Decoder) Length() (uint64, error) {
	n, err := d.backend.Length()
	if err != nil {
		return 0, err
	}
	if d.outputSampleRate == d.nativeSampleRate {
		return n, nil
	}
	return n * uint64(d.outputSampleRate) / uint64(d.nativeSampleRate), nil
}

// AvailableFrames mirrors ma_decoder_get_available_frames.
func (d *Decoder) AvailableFrames() (uint64, error) {
	length, err := d.Length()
	if err != nil {
		return 0, err
	}
	if d.readCursor >= length {
		return 0, nil
	}
	return length - d.readCursor, nil
}

/**************************************************************************
Encoder (ma_encoder)
**************************************************************************/

// EncoderConfig mirrors ma_encoder_config.
type EncoderConfig struct {
	EncodingFormat EncodingFormat
	Format         Format
	Channels       uint32
	SampleRate     uint32
}

// EncoderConfigInit mirrors ma_encoder_config_init.
func EncoderConfigInit(encodingFormat EncodingFormat, format Format, channels, sampleRate uint32) EncoderConfig {
	return EncoderConfig{EncodingFormat: encodingFormat, Format: format, Channels: channels, SampleRate: sampleRate}
}

// Encoder mirrors ma_encoder. Only WAV encoding is supported, matching
// miniaudio.
type Encoder struct {
	wav    *WAVEncoder
	closer io.Closer
}

// NewEncoder mirrors ma_encoder_init.
func NewEncoder(w io.WriteSeeker, config EncoderConfig) (*Encoder, error) {
	if config.EncodingFormat != EncodingFormatWAV {
		return nil, ErrFormatNotSupported
	}
	enc, err := NewWAVEncoder(w, config.Format, config.Channels, config.SampleRate)
	if err != nil {
		return nil, err
	}
	return &Encoder{wav: enc}, nil
}

// NewEncoderFile mirrors ma_encoder_init_file.
func NewEncoderFile(path string, config EncoderConfig) (*Encoder, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, ErrAccessDenied
	}
	enc, eerr := NewEncoder(f, config)
	if eerr != nil {
		f.Close()
		return nil, eerr
	}
	enc.closer = f
	return enc, nil
}

// WritePCMFrames mirrors ma_encoder_write_pcm_frames.
func (e *Encoder) WritePCMFrames(frames []byte, frameCount uint64) (uint64, error) {
	return e.wav.WritePCMFrames(frames, frameCount)
}

// Close mirrors ma_encoder_uninit: finalizes headers and releases resources.
func (e *Encoder) Close() error {
	err := e.wav.Close()
	if e.closer != nil {
		cerr := e.closer.Close()
		e.closer = nil
		if err == nil {
			err = cerr
		}
	}
	return err
}
