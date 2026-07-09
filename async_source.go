package gominiaudio

import (
	"sync"
	"sync/atomic"
)

// asyncSource wraps a DataSource (typically a file-backed Decoder) with a
// background feeder goroutine and a PCM ring buffer, so the audio thread
// never performs file I/O. A disk stall shows up as a short read (silence)
// instead of blocking the render callback.
//
// The reader (audio thread) only consumes from the ring; the feeder only
// produces into it. rbMu excludes the consumer during ring resets (seeks) -
// it is never held across I/O, so the audio thread can block on it only for
// the duration of a memcpy or ring reset.
//
// Looping is handled natively by the feeder (it seeks the source back to
// the start on end-of-source), so downstream readers see a continuous
// stream with no gap. Loop points and ranges from DataSourceController are
// not supported for async sources; fully decode with SoundFlagDecode when
// those are needed.
type asyncSource struct {
	src DataSource // Touched only by the feeder after init.

	format     Format
	channels   uint32
	sampleRate uint32
	bpf        int

	length    uint64 // Source length in frames; 0 when unknown.
	hasLength bool

	rb   *PCMRB
	rbMu sync.Mutex // Excludes the consumer during ring resets.

	looping   atomic.Bool
	srcEnd    atomic.Bool // Feeder reached the end of the source (and is not looping).
	delivered atomic.Uint64

	// Seek protocol: Seek publishes a target+sequence and kicks the feeder;
	// reads return 0 frames until the feeder has performed the source seek
	// and reset the ring.
	seekTarget atomic.Uint64
	seekReq    atomic.Uint32
	seekAck    atomic.Uint32

	wakeCh  chan struct{}
	stopped atomic.Bool
	doneCh  chan struct{}
}

const (
	// asyncRingFrames is the read-ahead capacity (~2/3s at 48kHz).
	asyncRingFrames = 32768
	// asyncChunkFrames is the feeder's decode granularity.
	asyncChunkFrames = 4096
)

// newAsyncSource wraps src. One chunk is decoded synchronously so playback
// can start immediately; the feeder keeps the ring filled from then on.
func newAsyncSource(src DataSource) (*asyncSource, error) {
	format, channels, rate, _, err := src.DataFormat()
	if err != nil {
		return nil, err
	}
	rb, err := NewPCMRB(format, channels, asyncRingFrames, nil)
	if err != nil {
		return nil, err
	}
	a := &asyncSource{
		src:        src,
		format:     format,
		channels:   channels,
		sampleRate: rate,
		bpf:        FrameSizeInBytes(format, channels),
		rb:         rb,
		wakeCh:     make(chan struct{}, 1),
		doneCh:     make(chan struct{}),
	}
	if n, lerr := src.Length(); lerr == nil {
		a.length = n
		a.hasLength = true
	}

	// Synchronous prefill of one chunk for gapless start.
	a.fillOnce(make([]byte, asyncChunkFrames*a.bpf))

	go a.feeder()
	return a, nil
}

// kick wakes the feeder without blocking.
func (a *asyncSource) kick() {
	select {
	case a.wakeCh <- struct{}{}:
	default:
	}
}

// stop shuts the feeder down. The underlying source must only be closed
// after stop returns.
func (a *asyncSource) stop() {
	if a.stopped.Swap(true) {
		return
	}
	a.kick()
	<-a.doneCh
}

// fillOnce decodes up to one chunk into the ring. Returns false when the
// source is exhausted and not looping. Feeder-side only.
func (a *asyncSource) fillOnce(buf []byte) bool {
	space := a.rb.AvailableWrite()
	if space == 0 {
		return true
	}
	frames := uint64(space)
	if frames > asyncChunkFrames {
		frames = asyncChunkFrames
	}

	read, err := a.src.Read(buf[:frames*uint64(a.bpf)], frames)

	// Copy into the ring (possibly split across the wrap point).
	remaining := read
	off := uint64(0)
	for remaining > 0 {
		chunk := a.rb.AcquireWrite(uint32(remaining))
		if len(chunk) == 0 {
			break
		}
		n := copy(chunk, buf[off*uint64(a.bpf):])
		_ = a.rb.CommitWrite(uint32(n / a.bpf))
		off += uint64(n / a.bpf)
		remaining -= uint64(n / a.bpf)
	}

	if err == ErrAtEnd {
		if a.looping.Load() {
			if serr := a.src.Seek(0); serr == nil {
				return true // Wrapped: keep filling seamlessly.
			}
		}
		a.srcEnd.Store(true)
		return false
	}
	return err == nil
}

// feeder is the background decode loop.
func (a *asyncSource) feeder() {
	defer close(a.doneCh)
	buf := make([]byte, asyncChunkFrames*a.bpf)

	for {
		if a.stopped.Load() {
			return
		}

		// Apply a pending seek: seek the source, then reset the ring with
		// the consumer excluded.
		if req := a.seekReq.Load(); req != a.seekAck.Load() {
			target := a.seekTarget.Load()
			_ = a.src.Seek(target)
			a.rbMu.Lock()
			a.rb.Reset()
			a.srcEnd.Store(false)
			a.delivered.Store(target)
			a.seekAck.Store(req)
			a.rbMu.Unlock()
			continue
		}

		if a.srcEnd.Load() || a.rb.AvailableWrite() < asyncChunkFrames {
			// Nothing to do until the reader consumes or a seek arrives.
			<-a.wakeCh
			continue
		}

		a.fillOnce(buf)
	}
}

/* DataSource implementation (reader side). */

// Read implements DataSource. It never blocks on I/O: when the ring is
// empty it returns the frames it has (possibly zero) without error, and
// only reports ErrAtEnd once the source is truly exhausted and drained.
func (a *asyncSource) Read(framesOut []byte, frameCount uint64) (uint64, error) {
	if a.seekReq.Load() != a.seekAck.Load() {
		a.kick()
		return 0, nil // Seek in flight; data not yet available.
	}

	a.rbMu.Lock()
	var total uint64
	for total < frameCount {
		chunk := a.rb.AcquireRead(uint32(frameCount - total))
		if len(chunk) == 0 {
			break
		}
		frames := uint64(len(chunk) / a.bpf)
		if framesOut != nil {
			copy(framesOut[total*uint64(a.bpf):], chunk)
		}
		_ = a.rb.CommitRead(uint32(frames))
		total += frames
	}
	a.rbMu.Unlock()

	a.delivered.Add(total)
	a.kick()

	if total < frameCount && a.srcEnd.Load() && a.rb.AvailableRead() == 0 {
		return total, ErrAtEnd
	}
	return total, nil
}

// Seek implements DataSource. The seek is asynchronous: subsequent reads
// return no data until the feeder has repositioned the source.
func (a *asyncSource) Seek(frameIndex uint64) error {
	a.seekTarget.Store(frameIndex)
	a.seekReq.Add(1)
	a.kick()
	return nil
}

// DataFormat implements DataSource.
func (a *asyncSource) DataFormat() (Format, uint32, uint32, []Channel, error) {
	return a.format, a.channels, a.sampleRate, nil, nil
}

// Cursor implements DataSource. Reported in source frames; wraps with the
// source length when looping.
func (a *asyncSource) Cursor() (uint64, error) {
	d := a.delivered.Load()
	if a.hasLength && a.length > 0 {
		if a.looping.Load() {
			return d % a.length, nil
		}
		if d > a.length {
			return a.length, nil
		}
	}
	return d, nil
}

// Length implements DataSource.
func (a *asyncSource) Length() (uint64, error) {
	if !a.hasLength {
		return 0, ErrNotImplemented
	}
	return a.length, nil
}

// SetLooping implements LoopingDataSource: the feeder wraps seamlessly at
// the end of the source.
func (a *asyncSource) SetLooping(looping bool) error {
	a.looping.Store(looping)
	if looping && a.srcEnd.Load() {
		// Already stopped at the end: restart from the beginning.
		return a.Seek(0)
	}
	a.kick()
	return nil
}
