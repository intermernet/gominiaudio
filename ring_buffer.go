package gominiaudio

import "sync/atomic"

// RB mirrors ma_rb: a lock-free single-producer single-consumer ring buffer
// operating on bytes. The read and write offsets encode a "loop flag" in the
// most significant bit which disambiguates the full and empty states, exactly
// like miniaudio.
type RB struct {
	buffer         []byte
	sizeInBytes    uint32
	encodedRead    atomic.Uint32 // Most significant bit is the loop flag; remaining bits are the offset.
	encodedWrite   atomic.Uint32
	subbufferSizeInBytes  uint32
	subbufferCount        uint32
	subbufferStrideInBytes uint32
}

const rbLoopFlag = uint32(0x80000000)

func rbExtractOffsetLoopFlag(encoded uint32) (offset, loopFlag uint32) {
	return encoded & 0x7FFFFFFF, encoded & rbLoopFlag
}

func rbConstruct(offset, loopFlag uint32) uint32 {
	return (offset & 0x7FFFFFFF) | (loopFlag & rbLoopFlag)
}

// NewRB mirrors ma_rb_init: a ring buffer of the given size. If
// preallocatedBuffer is non-nil it is used directly (its length must be at
// least sizeInBytes); otherwise a buffer is allocated.
func NewRB(sizeInBytes uint32, preallocatedBuffer []byte) (*RB, error) {
	return NewRBEx(sizeInBytes, 1, 0, preallocatedBuffer)
}

// NewRBEx mirrors ma_rb_init_ex with subbuffer support.
func NewRBEx(subbufferSizeInBytes, subbufferCount, subbufferStrideInBytes uint32, preallocatedBuffer []byte) (*RB, error) {
	if subbufferSizeInBytes == 0 || subbufferCount == 0 {
		return nil, ErrInvalidArgs
	}
	if subbufferSizeInBytes > 0x7FFFFFFF {
		return nil, ErrInvalidArgs
	}
	if subbufferStrideInBytes == 0 {
		subbufferStrideInBytes = subbufferSizeInBytes
	}
	total := subbufferStrideInBytes * subbufferCount
	rb := &RB{
		sizeInBytes:            total,
		subbufferSizeInBytes:   subbufferSizeInBytes,
		subbufferCount:         subbufferCount,
		subbufferStrideInBytes: subbufferStrideInBytes,
	}
	if preallocatedBuffer != nil {
		if uint32(len(preallocatedBuffer)) < total {
			return nil, ErrInvalidArgs
		}
		rb.buffer = preallocatedBuffer[:total]
	} else {
		rb.buffer = make([]byte, total)
	}
	return rb, nil
}

// Reset mirrors ma_rb_reset. Not thread safe.
func (rb *RB) Reset() {
	rb.encodedRead.Store(0)
	rb.encodedWrite.Store(0)
}

// AcquireRead mirrors ma_rb_acquire_read: returns a slice of readable bytes,
// up to sizeRequested. Commit with CommitRead.
func (rb *RB) AcquireRead(sizeRequested uint32) []byte {
	readOffset, readLoop := rbExtractOffsetLoopFlag(rb.encodedRead.Load())
	writeOffset, writeLoop := rbExtractOffsetLoopFlag(rb.encodedWrite.Load())

	var available uint32
	if readLoop == writeLoop {
		available = writeOffset - readOffset
	} else {
		available = rb.sizeInBytes - readOffset
	}
	n := min(sizeRequested, available)
	return rb.buffer[readOffset : readOffset+n]
}

// CommitRead mirrors ma_rb_commit_read. sizeInBytes must be no more than the
// size of the slice previously acquired.
func (rb *RB) CommitRead(sizeInBytes uint32) error {
	encoded := rb.encodedRead.Load()
	offset, loop := rbExtractOffsetLoopFlag(encoded)
	newOffset := offset + sizeInBytes
	if newOffset > rb.sizeInBytes {
		return ErrInvalidArgs
	}
	if newOffset == rb.sizeInBytes {
		newOffset = 0
		loop ^= rbLoopFlag
	}
	rb.encodedRead.Store(rbConstruct(newOffset, loop))
	if sizeInBytes == 0 {
		return nil
	}
	return nil
}

// AcquireWrite mirrors ma_rb_acquire_write: returns a slice of writable
// bytes, up to sizeRequested. Commit with CommitWrite.
func (rb *RB) AcquireWrite(sizeRequested uint32) []byte {
	readOffset, readLoop := rbExtractOffsetLoopFlag(rb.encodedRead.Load())
	writeOffset, writeLoop := rbExtractOffsetLoopFlag(rb.encodedWrite.Load())

	var available uint32
	if writeLoop == readLoop {
		available = rb.sizeInBytes - writeOffset
	} else {
		available = readOffset - writeOffset
	}
	n := min(sizeRequested, available)
	return rb.buffer[writeOffset : writeOffset+n]
}

// CommitWrite mirrors ma_rb_commit_write.
func (rb *RB) CommitWrite(sizeInBytes uint32) error {
	encoded := rb.encodedWrite.Load()
	offset, loop := rbExtractOffsetLoopFlag(encoded)
	newOffset := offset + sizeInBytes
	if newOffset > rb.sizeInBytes {
		return ErrInvalidArgs
	}
	if newOffset == rb.sizeInBytes {
		newOffset = 0
		loop ^= rbLoopFlag
	}
	rb.encodedWrite.Store(rbConstruct(newOffset, loop))
	return nil
}

// Seek mirrors ma_rb_seek_read/ma_rb_seek_write combined: SeekRead advances
// the read pointer without reading.
func (rb *RB) SeekRead(offsetInBytes uint32) error {
	for offsetInBytes > 0 {
		chunk := rb.AcquireRead(offsetInBytes)
		if len(chunk) == 0 {
			return ErrInvalidArgs
		}
		if err := rb.CommitRead(uint32(len(chunk))); err != nil {
			return err
		}
		offsetInBytes -= uint32(len(chunk))
	}
	return nil
}

// SeekWrite advances the write pointer without writing.
func (rb *RB) SeekWrite(offsetInBytes uint32) error {
	for offsetInBytes > 0 {
		chunk := rb.AcquireWrite(offsetInBytes)
		if len(chunk) == 0 {
			return ErrInvalidArgs
		}
		if err := rb.CommitWrite(uint32(len(chunk))); err != nil {
			return err
		}
		offsetInBytes -= uint32(len(chunk))
	}
	return nil
}

// PointerDistance mirrors ma_rb_pointer_distance: the number of bytes
// between the read and write pointers (i.e. readable bytes, accounting for
// wraparound).
func (rb *RB) PointerDistance() int32 {
	readOffset, readLoop := rbExtractOffsetLoopFlag(rb.encodedRead.Load())
	writeOffset, writeLoop := rbExtractOffsetLoopFlag(rb.encodedWrite.Load())
	if readLoop == writeLoop {
		return int32(writeOffset) - int32(readOffset)
	}
	return int32(writeOffset) + (int32(rb.sizeInBytes) - int32(readOffset))
}

// AvailableRead mirrors ma_rb_available_read.
func (rb *RB) AvailableRead() uint32 {
	d := rb.PointerDistance()
	if d < 0 {
		return 0
	}
	return uint32(d)
}

// AvailableWrite mirrors ma_rb_available_write.
func (rb *RB) AvailableWrite() uint32 {
	return rb.sizeInBytes - rb.AvailableRead()
}

// SizeInBytes returns the buffer capacity in bytes.
func (rb *RB) SizeInBytes() uint32 { return rb.sizeInBytes }

// PCMRB mirrors ma_pcm_rb: a ring buffer operating on PCM frames.
type PCMRB struct {
	rb         *RB
	format     Format
	channels   uint32
	sampleRate uint32 // Not required, but useful for cases where the sample rate is needed alongside the data.
}

// NewPCMRB mirrors ma_pcm_rb_init.
func NewPCMRB(format Format, channels, subbufferSizeInFrames uint32, preallocatedBuffer []byte) (*PCMRB, error) {
	return NewPCMRBEx(format, channels, subbufferSizeInFrames, 1, 0, preallocatedBuffer)
}

// NewPCMRBEx mirrors ma_pcm_rb_init_ex.
func NewPCMRBEx(format Format, channels, subbufferSizeInFrames, subbufferCount, subbufferStrideInFrames uint32, preallocatedBuffer []byte) (*PCMRB, error) {
	bpf := uint32(FrameSizeInBytes(format, channels))
	if bpf == 0 {
		return nil, ErrInvalidArgs
	}
	rb, err := NewRBEx(subbufferSizeInFrames*bpf, subbufferCount, subbufferStrideInFrames*bpf, preallocatedBuffer)
	if err != nil {
		return nil, err
	}
	return &PCMRB{rb: rb, format: format, channels: channels}, nil
}

func (p *PCMRB) bpf() uint32 { return uint32(FrameSizeInBytes(p.format, p.channels)) }

// Reset mirrors ma_pcm_rb_reset.
func (p *PCMRB) Reset() { p.rb.Reset() }

// AcquireRead mirrors ma_pcm_rb_acquire_read: returns readable frames as a
// byte slice, up to frameCount frames.
func (p *PCMRB) AcquireRead(frameCount uint32) []byte {
	buf := p.rb.AcquireRead(frameCount * p.bpf())
	// Truncate to a whole number of frames.
	n := uint32(len(buf)) / p.bpf() * p.bpf()
	return buf[:n]
}

// CommitRead mirrors ma_pcm_rb_commit_read.
func (p *PCMRB) CommitRead(frameCount uint32) error {
	return p.rb.CommitRead(frameCount * p.bpf())
}

// AcquireWrite mirrors ma_pcm_rb_acquire_write.
func (p *PCMRB) AcquireWrite(frameCount uint32) []byte {
	buf := p.rb.AcquireWrite(frameCount * p.bpf())
	n := uint32(len(buf)) / p.bpf() * p.bpf()
	return buf[:n]
}

// CommitWrite mirrors ma_pcm_rb_commit_write.
func (p *PCMRB) CommitWrite(frameCount uint32) error {
	return p.rb.CommitWrite(frameCount * p.bpf())
}

// PointerDistance mirrors ma_pcm_rb_pointer_distance, in frames.
func (p *PCMRB) PointerDistance() int32 {
	return p.rb.PointerDistance() / int32(p.bpf())
}

// AvailableRead mirrors ma_pcm_rb_available_read, in frames.
func (p *PCMRB) AvailableRead() uint32 {
	return p.rb.AvailableRead() / p.bpf()
}

// AvailableWrite mirrors ma_pcm_rb_available_write, in frames.
func (p *PCMRB) AvailableWrite() uint32 {
	return p.rb.AvailableWrite() / p.bpf()
}

// SeekRead mirrors ma_pcm_rb_seek_read, in frames.
func (p *PCMRB) SeekRead(frameCount uint32) error {
	return p.rb.SeekRead(frameCount * p.bpf())
}

// SeekWrite mirrors ma_pcm_rb_seek_write, in frames.
func (p *PCMRB) SeekWrite(frameCount uint32) error {
	return p.rb.SeekWrite(frameCount * p.bpf())
}

// Format returns the sample format.
func (p *PCMRB) Format() Format { return p.format }

// Channels returns the channel count.
func (p *PCMRB) Channels() uint32 { return p.channels }

// SampleRate mirrors ma_pcm_rb_get_sample_rate.
func (p *PCMRB) SampleRate() uint32 { return p.sampleRate }

// SetSampleRate mirrors ma_pcm_rb_set_sample_rate.
func (p *PCMRB) SetSampleRate(rate uint32) { p.sampleRate = rate }

// DuplexRB mirrors ma_duplex_rb: a ring buffer intended for duplex devices,
// where the capture side produces and the playback side consumes.
type DuplexRB struct {
	rb *PCMRB
}

// NewDuplexRB mirrors ma_duplex_rb_init. captureInternalFormat describes the
// data written by the capture side; the buffer holds roughly one second. The
// write pointer is pre-seeked by two capture periods, matching miniaudio.
func NewDuplexRB(captureFormat Format, captureChannels, sampleRate, captureInternalPeriodSizeInFrames uint32) (*DuplexRB, error) {
	return NewDuplexRBEx(captureFormat, captureChannels, sampleRate, captureInternalPeriodSizeInFrames, captureInternalPeriodSizeInFrames*2)
}

// NewDuplexRBEx is NewDuplexRB with an explicit pre-seek amount. The
// pre-seek is silence inserted between the capture and playback sides: it
// directly adds preSeekInFrames of latency but absorbs scheduling jitter
// between the two streams. One capture period is the practical minimum when
// both directions share a clock.
func NewDuplexRBEx(captureFormat Format, captureChannels, sampleRate, captureInternalPeriodSizeInFrames, preSeekInFrames uint32) (*DuplexRB, error) {
	sizeInFrames := sampleRate + captureInternalPeriodSizeInFrames // ~1 second plus one period of headroom.
	rb, err := NewPCMRB(captureFormat, captureChannels, sizeInFrames, nil)
	if err != nil {
		return nil, err
	}
	rb.SetSampleRate(sampleRate)
	// Seek the write pointer ahead to introduce a small startup latency
	// buffer, mirroring miniaudio's duplex_rb behavior.
	_ = rb.SeekWrite(preSeekInFrames)
	return &DuplexRB{rb: rb}, nil
}

// RB returns the underlying PCM ring buffer.
func (d *DuplexRB) RB() *PCMRB { return d.rb }
