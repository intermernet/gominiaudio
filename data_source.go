package gominiaudio

// DataSource mirrors ma_data_source. A data source is anything that PCM
// frames can be read from: decoders, waveforms, noise generators, audio
// buffers, engine sounds.
//
// Read fills framesOut with up to frameCount frames in the source's native
// data format and returns the number of frames actually read. When the end
// is reached, Read returns the frames read so far together with ErrAtEnd
// (framesRead may be non-zero alongside the error, mirroring
// MA_AT_END semantics).
type DataSource interface {
	// Read reads up to frameCount frames into framesOut. framesOut may be
	// nil to seek forward. Returns ErrAtEnd when the source is exhausted.
	Read(framesOut []byte, frameCount uint64) (framesRead uint64, err error)

	// Seek mirrors ma_data_source_seek_to_pcm_frame.
	Seek(frameIndex uint64) error

	// DataFormat mirrors ma_data_source_get_data_format.
	DataFormat() (format Format, channels uint32, sampleRate uint32, channelMap []Channel, err error)

	// Cursor mirrors ma_data_source_get_cursor_in_pcm_frames.
	Cursor() (uint64, error)

	// Length mirrors ma_data_source_get_length_in_pcm_frames. Returns
	// ErrNotImplemented for endless sources (waveform, noise) and unknown
	// lengths.
	Length() (uint64, error)
}

// LoopingDataSource is implemented by sources that natively support looping,
// mirroring the onSetLooping vtable callback.
type LoopingDataSource interface {
	DataSource
	SetLooping(looping bool) error
}

// DataSourceController wraps a DataSource with the management features of
// ma_data_source_base: ranges, loop points, looping and chaining. All
// ma_data_source_* functions that operate on the base are methods here.
type DataSourceController struct {
	ds DataSource

	rangeBegInFrames     uint64
	rangeEndInFrames     uint64 // Set to ^uint64(0) for unranged.
	loopBegInFrames      uint64
	loopEndInFrames      uint64 // Set to ^uint64(0) to loop to the end of the range.
	isLooping            bool

	current *DataSourceController // Chaining, mirroring ma_data_source_get_current.
	next    *DataSourceController
	nextFn  func() *DataSourceController
}

const noEnd = ^uint64(0)

// NewDataSourceController wraps ds.
func NewDataSourceController(ds DataSource) *DataSourceController {
	c := &DataSourceController{
		ds:               ds,
		rangeEndInFrames: noEnd,
		loopEndInFrames:  noEnd,
	}
	c.current = c
	return c
}

// Source returns the wrapped data source.
func (c *DataSourceController) Source() DataSource { return c.ds }

// SetLooping mirrors ma_data_source_set_looping.
func (c *DataSourceController) SetLooping(looping bool) error {
	c.isLooping = looping
	if l, ok := c.ds.(LoopingDataSource); ok {
		return l.SetLooping(looping)
	}
	return nil
}

// IsLooping mirrors ma_data_source_is_looping.
func (c *DataSourceController) IsLooping() bool { return c.isLooping }

// SetRangeInPCMFrames mirrors ma_data_source_set_range_in_pcm_frames.
func (c *DataSourceController) SetRangeInPCMFrames(rangeBeg, rangeEnd uint64) error {
	if rangeEnd < rangeBeg {
		return ErrInvalidArgs
	}
	relativeCursor, err := c.ds.Cursor()
	hasCursor := err == nil
	absoluteCursor := c.rangeBegInFrames + relativeCursor

	c.rangeBegInFrames = rangeBeg
	c.rangeEndInFrames = rangeEnd

	// Keep the cursor within the new range where possible.
	if hasCursor {
		newRelative := uint64(0)
		if absoluteCursor > rangeBeg {
			newRelative = absoluteCursor - rangeBeg
		}
		if rangeEnd != noEnd && rangeBeg+newRelative > rangeEnd {
			newRelative = rangeEnd - rangeBeg
		}
		_ = c.ds.Seek(rangeBeg + newRelative)
	}
	return nil
}

// RangeInPCMFrames mirrors ma_data_source_get_range_in_pcm_frames.
func (c *DataSourceController) RangeInPCMFrames() (rangeBeg, rangeEnd uint64) {
	return c.rangeBegInFrames, c.rangeEndInFrames
}

// SetLoopPointInPCMFrames mirrors ma_data_source_set_loop_point_in_pcm_frames.
// Loop points are relative to the start of the range.
func (c *DataSourceController) SetLoopPointInPCMFrames(loopBeg, loopEnd uint64) error {
	if loopEnd < loopBeg {
		return ErrInvalidArgs
	}
	c.loopBegInFrames = loopBeg
	c.loopEndInFrames = loopEnd
	return nil
}

// LoopPointInPCMFrames mirrors ma_data_source_get_loop_point_in_pcm_frames.
func (c *DataSourceController) LoopPointInPCMFrames() (loopBeg, loopEnd uint64) {
	return c.loopBegInFrames, c.loopEndInFrames
}

// SetNext mirrors ma_data_source_set_next.
func (c *DataSourceController) SetNext(next *DataSourceController) { c.next = next }

// Next mirrors ma_data_source_get_next.
func (c *DataSourceController) Next() *DataSourceController { return c.next }

// SetNextCallback mirrors ma_data_source_set_next_callback.
func (c *DataSourceController) SetNextCallback(fn func() *DataSourceController) { c.nextFn = fn }

// Current mirrors ma_data_source_get_current: the source currently being
// read from in a chain.
func (c *DataSourceController) Current() *DataSourceController { return c.current }

// Seek mirrors ma_data_source_seek_to_pcm_frame, relative to the range start.
func (c *DataSourceController) Seek(frameIndex uint64) error {
	if c.rangeEndInFrames != noEnd && c.rangeBegInFrames+frameIndex > c.rangeEndInFrames {
		return ErrInvalidOperation
	}
	return c.ds.Seek(c.rangeBegInFrames + frameIndex)
}

// SeekSeconds mirrors ma_data_source_seek_seconds.
func (c *DataSourceController) SeekSeconds(seconds float32) error {
	_, _, sampleRate, _, err := c.ds.DataFormat()
	if err != nil {
		return err
	}
	return c.Seek(uint64(float64(seconds) * float64(sampleRate)))
}

// Cursor mirrors ma_data_source_get_cursor_in_pcm_frames, relative to the
// range start.
func (c *DataSourceController) Cursor() (uint64, error) {
	cur, err := c.ds.Cursor()
	if err != nil {
		return 0, err
	}
	if cur < c.rangeBegInFrames {
		return 0, nil
	}
	return cur - c.rangeBegInFrames, nil
}

// CursorInSeconds mirrors ma_data_source_get_cursor_in_seconds.
func (c *DataSourceController) CursorInSeconds() (float32, error) {
	cur, err := c.Cursor()
	if err != nil {
		return 0, err
	}
	_, _, sampleRate, _, err := c.ds.DataFormat()
	if err != nil {
		return 0, err
	}
	return float32(cur) / float32(sampleRate), nil
}

// Length mirrors ma_data_source_get_length_in_pcm_frames, taking the range
// into account.
func (c *DataSourceController) Length() (uint64, error) {
	length, err := c.ds.Length()
	if err != nil {
		return 0, err
	}
	if length < c.rangeBegInFrames {
		return 0, nil
	}
	length -= c.rangeBegInFrames
	if c.rangeEndInFrames != noEnd {
		rangeLen := c.rangeEndInFrames - c.rangeBegInFrames
		if length > rangeLen {
			length = rangeLen
		}
	}
	return length, nil
}

// LengthInSeconds mirrors ma_data_source_get_length_in_seconds.
func (c *DataSourceController) LengthInSeconds() (float32, error) {
	length, err := c.Length()
	if err != nil {
		return 0, err
	}
	_, _, sampleRate, _, err := c.ds.DataFormat()
	if err != nil {
		return 0, err
	}
	return float32(length) / float32(sampleRate), nil
}

// DataFormat mirrors ma_data_source_get_data_format.
func (c *DataSourceController) DataFormat() (Format, uint32, uint32, []Channel, error) {
	return c.ds.DataFormat()
}

// Read mirrors ma_data_source_read_pcm_frames with full range, loop point
// and chaining semantics.
func (c *DataSourceController) Read(framesOut []byte, frameCount uint64) (uint64, error) {
	if frameCount == 0 {
		return 0, ErrInvalidArgs
	}

	format, channels, _, _, err := c.ds.DataFormat()
	if err != nil {
		return 0, err
	}
	bpf := uint64(FrameSizeInBytes(format, channels))

	var totalRead uint64
	for totalRead < frameCount {
		cur := c.current
		if cur == nil {
			break
		}

		framesRemaining := frameCount - totalRead

		// Clamp to the loop/range end.
		loopEnd := cur.effectiveEndInFrames()
		if loopEnd != noEnd {
			absCursor, err := cur.ds.Cursor()
			if err == nil && absCursor < loopEnd {
				if remaining := loopEnd - absCursor; framesRemaining > remaining {
					framesRemaining = remaining
				}
			}
		}

		var dst []byte
		if framesOut != nil {
			dst = framesOut[totalRead*bpf:]
		}

		read, readErr := cur.ds.Read(dst, framesRemaining)
		totalRead += read

		// Only ErrAtEnd means the source is exhausted. A short read without
		// it means no more data is available right now (e.g. an async
		// source refilling): return the partial read instead of looping.
		atEnd := readErr == ErrAtEnd
		if readErr != nil && readErr != ErrAtEnd {
			return totalRead, readErr
		}

		// Check whether the loop end has been reached.
		if loopEnd != noEnd {
			if absCursor, err := cur.ds.Cursor(); err == nil && absCursor >= loopEnd {
				atEnd = true
			}
		}

		if !atEnd {
			if read == 0 {
				break // No data currently available; partial result.
			}
			if totalRead >= frameCount {
				break
			}
			continue
		}

		// End of the current source: loop, chain, or stop.
		if cur.isLooping {
			loopBeg := cur.rangeBegInFrames + cur.loopBegInFrames
			if err := cur.ds.Seek(loopBeg); err != nil {
				return totalRead, err
			}
			continue
		}

		next := cur.next
		if next == nil && cur.nextFn != nil {
			next = cur.nextFn()
		}
		if next != nil {
			c.current = next
			if err := next.ds.Seek(next.rangeBegInFrames); err != nil {
				return totalRead, err
			}
			continue
		}

		return totalRead, ErrAtEnd
	}

	return totalRead, nil
}

// effectiveEndInFrames returns the absolute frame index at which reading
// should stop for looping/range purposes, or noEnd.
func (c *DataSourceController) effectiveEndInFrames() uint64 {
	if c.isLooping && c.loopEndInFrames != noEnd {
		return c.rangeBegInFrames + c.loopEndInFrames
	}
	return c.rangeEndInFrames
}
