package gominiaudio

import (
	"encoding/binary"
	"io"
)

// This file implements a native WAV (RIFF WAVE) decoder and encoder,
// replacing miniaudio's bundled dr_wav. Supported sample formats: PCM
// u8/s16/s24/s32 and IEEE float f32 (including WAVE_FORMAT_EXTENSIBLE
// wrappers of both).

const (
	wavFormatPCM        = 0x0001
	wavFormatIEEEFloat  = 0x0003
	wavFormatALaw       = 0x0006
	wavFormatMuLaw      = 0x0007
	wavFormatExtensible = 0xFFFE
)

// WAVDecoder decodes a WAV stream from an io.ReadSeeker. It implements
// DataSource, producing frames in the file's native format.
type WAVDecoder struct {
	r io.ReadSeeker

	format        Format
	channels      uint32
	sampleRate    uint32
	bitsPerSample uint16
	formatTag     uint16
	channelMask   uint32

	dataOffset int64  // Byte offset of the start of sample data.
	dataSize   uint64 // Size of the data chunk in bytes.
	cursor     uint64 // Current position in frames.
	totalFrames uint64
}

// NewWAVDecoder mirrors initializing a wav decoding backend: it parses the
// RIFF header and prepares for reading sample data.
func NewWAVDecoder(r io.ReadSeeker) (*WAVDecoder, error) {
	d := &WAVDecoder{r: r}
	if err := d.parseHeader(); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *WAVDecoder) parseHeader() error {
	var riff [12]byte
	if _, err := io.ReadFull(d.r, riff[:]); err != nil {
		return ErrInvalidFile
	}
	if string(riff[0:4]) != "RIFF" || string(riff[8:12]) != "WAVE" {
		return ErrInvalidFile
	}

	var haveFmt, haveData bool
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(d.r, hdr[:]); err != nil {
			break
		}
		chunkID := string(hdr[0:4])
		chunkSize := binary.LittleEndian.Uint32(hdr[4:8])

		switch chunkID {
		case "fmt ":
			buf := make([]byte, chunkSize)
			if _, err := io.ReadFull(d.r, buf); err != nil {
				return ErrInvalidFile
			}
			if chunkSize < 16 {
				return ErrInvalidFile
			}
			d.formatTag = binary.LittleEndian.Uint16(buf[0:2])
			d.channels = uint32(binary.LittleEndian.Uint16(buf[2:4]))
			d.sampleRate = binary.LittleEndian.Uint32(buf[4:8])
			d.bitsPerSample = binary.LittleEndian.Uint16(buf[14:16])
			if d.formatTag == wavFormatExtensible && chunkSize >= 40 {
				// cbSize(2) validBits(2) channelMask(4) subFormat GUID(16).
				d.channelMask = binary.LittleEndian.Uint32(buf[20:24])
				d.formatTag = binary.LittleEndian.Uint16(buf[24:26]) // First 2 bytes of the sub-format GUID.
			}
			haveFmt = true
		case "data":
			d.dataSize = uint64(chunkSize)
			off, err := d.r.Seek(0, io.SeekCurrent)
			if err != nil {
				return ErrBadSeek
			}
			d.dataOffset = off
			haveData = true
			// Skip past the data chunk in case more chunks follow; chunks
			// are word aligned.
			skip := int64(chunkSize)
			if chunkSize%2 == 1 {
				skip++
			}
			if _, err := d.r.Seek(skip, io.SeekCurrent); err != nil {
				// A stream that can't seek past the end is fine if we
				// already have everything we need.
				if haveFmt {
					break
				}
				return ErrBadSeek
			}
		default:
			skip := int64(chunkSize)
			if chunkSize%2 == 1 {
				skip++
			}
			if _, err := d.r.Seek(skip, io.SeekCurrent); err != nil {
				return ErrBadSeek
			}
		}

		if haveFmt && haveData {
			// Keep scanning remaining chunks is unnecessary for playback.
			break
		}
	}

	if !haveFmt || !haveData {
		return ErrInvalidFile
	}
	if d.channels == 0 || d.sampleRate == 0 {
		return ErrInvalidFile
	}

	switch d.formatTag {
	case wavFormatPCM:
		switch d.bitsPerSample {
		case 8:
			d.format = FormatU8
		case 16:
			d.format = FormatS16
		case 24:
			d.format = FormatS24
		case 32:
			d.format = FormatS32
		default:
			return ErrFormatNotSupported
		}
	case wavFormatIEEEFloat:
		if d.bitsPerSample != 32 {
			return ErrFormatNotSupported
		}
		d.format = FormatF32
	default:
		return ErrFormatNotSupported
	}

	bpf := uint64(FrameSizeInBytes(d.format, d.channels))
	d.totalFrames = d.dataSize / bpf

	// Position at the start of sample data.
	if _, err := d.r.Seek(d.dataOffset, io.SeekStart); err != nil {
		return ErrBadSeek
	}
	return nil
}

// Read implements DataSource: reads frames in the file's native format.
func (d *WAVDecoder) Read(framesOut []byte, frameCount uint64) (uint64, error) {
	if d.cursor >= d.totalFrames {
		return 0, ErrAtEnd
	}
	n := min(frameCount, d.totalFrames-d.cursor)
	bpf := uint64(FrameSizeInBytes(d.format, d.channels))

	if framesOut == nil {
		if err := d.Seek(d.cursor + n); err != nil {
			return 0, err
		}
		if n < frameCount {
			return n, ErrAtEnd
		}
		return n, nil
	}

	read, err := io.ReadFull(d.r, framesOut[:n*bpf])
	framesRead := uint64(read) / bpf
	d.cursor += framesRead
	if err != nil && framesRead == 0 {
		return 0, ErrAtEnd
	}
	if framesRead < frameCount {
		return framesRead, ErrAtEnd
	}
	return framesRead, nil
}

// Seek implements DataSource.
func (d *WAVDecoder) Seek(frameIndex uint64) error {
	if frameIndex > d.totalFrames {
		return ErrInvalidArgs
	}
	bpf := uint64(FrameSizeInBytes(d.format, d.channels))
	if _, err := d.r.Seek(d.dataOffset+int64(frameIndex*bpf), io.SeekStart); err != nil {
		return ErrBadSeek
	}
	d.cursor = frameIndex
	return nil
}

// DataFormat implements DataSource.
func (d *WAVDecoder) DataFormat() (Format, uint32, uint32, []Channel, error) {
	return d.format, d.channels, d.sampleRate, nil, nil
}

// Cursor implements DataSource.
func (d *WAVDecoder) Cursor() (uint64, error) { return d.cursor, nil }

// Length implements DataSource.
func (d *WAVDecoder) Length() (uint64, error) { return d.totalFrames, nil }

/**************************************************************************
WAV encoder
**************************************************************************/

// WAVEncoder writes a WAV file to an io.WriteSeeker. Finalize with Close,
// which patches the RIFF sizes.
type WAVEncoder struct {
	w             io.WriteSeeker
	format        Format
	channels      uint32
	sampleRate    uint32
	framesWritten uint64
	headerWritten bool
}

// NewWAVEncoder creates a WAV encoder for the given output format.
func NewWAVEncoder(w io.WriteSeeker, format Format, channels, sampleRate uint32) (*WAVEncoder, error) {
	if format == FormatUnknown || channels == 0 || sampleRate == 0 {
		return nil, ErrInvalidArgs
	}
	e := &WAVEncoder{w: w, format: format, channels: channels, sampleRate: sampleRate}
	if err := e.writeHeader(); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *WAVEncoder) writeHeader() error {
	var formatTag uint16 = wavFormatPCM
	if e.format == FormatF32 {
		formatTag = wavFormatIEEEFloat
	}
	bps := uint16(e.format.SizeInBytes() * 8)
	blockAlign := uint16(FrameSizeInBytes(e.format, e.channels))
	byteRate := e.sampleRate * uint32(blockAlign)

	var hdr [44]byte
	copy(hdr[0:4], "RIFF")
	// Sizes at offsets 4 and 40 are patched in Close.
	copy(hdr[8:12], "WAVE")
	copy(hdr[12:16], "fmt ")
	binary.LittleEndian.PutUint32(hdr[16:20], 16)
	binary.LittleEndian.PutUint16(hdr[20:22], formatTag)
	binary.LittleEndian.PutUint16(hdr[22:24], uint16(e.channels))
	binary.LittleEndian.PutUint32(hdr[24:28], e.sampleRate)
	binary.LittleEndian.PutUint32(hdr[28:32], byteRate)
	binary.LittleEndian.PutUint16(hdr[32:34], blockAlign)
	binary.LittleEndian.PutUint16(hdr[34:36], bps)
	copy(hdr[36:40], "data")

	if _, err := e.w.Write(hdr[:]); err != nil {
		return ErrIOError
	}
	e.headerWritten = true
	return nil
}

// WritePCMFrames mirrors ma_encoder_write_pcm_frames. frames must be in the
// encoder's format.
func (e *WAVEncoder) WritePCMFrames(frames []byte, frameCount uint64) (uint64, error) {
	bpf := uint64(FrameSizeInBytes(e.format, e.channels))
	n, err := e.w.Write(frames[:frameCount*bpf])
	written := uint64(n) / bpf
	e.framesWritten += written
	if err != nil {
		return written, ErrIOError
	}
	return written, nil
}

// Close finalizes the file by patching the RIFF and data chunk sizes.
func (e *WAVEncoder) Close() error {
	bpf := uint64(FrameSizeInBytes(e.format, e.channels))
	dataSize := e.framesWritten * bpf
	riffSize := 36 + dataSize

	var b4 [4]byte
	if _, err := e.w.Seek(4, io.SeekStart); err != nil {
		return ErrBadSeek
	}
	binary.LittleEndian.PutUint32(b4[:], uint32(riffSize))
	if _, err := e.w.Write(b4[:]); err != nil {
		return ErrIOError
	}
	if _, err := e.w.Seek(40, io.SeekStart); err != nil {
		return ErrBadSeek
	}
	binary.LittleEndian.PutUint32(b4[:], uint32(dataSize))
	if _, err := e.w.Write(b4[:]); err != nil {
		return ErrIOError
	}
	if _, err := e.w.Seek(0, io.SeekEnd); err != nil {
		return ErrBadSeek
	}
	return nil
}
