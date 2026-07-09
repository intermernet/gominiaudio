package gominiaudio

import "math"

// WaveformType mirrors ma_waveform_type.
type WaveformType uint32

const (
	WaveformTypeSine     WaveformType = 0
	WaveformTypeSquare   WaveformType = 1
	WaveformTypeTriangle WaveformType = 2
	WaveformTypeSawtooth WaveformType = 3
)

// WaveformConfig mirrors ma_waveform_config.
type WaveformConfig struct {
	Format     Format
	Channels   uint32
	SampleRate uint32
	Type       WaveformType
	Amplitude  float64
	Frequency  float64
}

// WaveformConfigInit mirrors ma_waveform_config_init.
func WaveformConfigInit(format Format, channels, sampleRate uint32, typ WaveformType, amplitude, frequency float64) WaveformConfig {
	return WaveformConfig{Format: format, Channels: channels, SampleRate: sampleRate, Type: typ, Amplitude: amplitude, Frequency: frequency}
}

// Waveform mirrors ma_waveform: an endless waveform generator implementing
// DataSource.
type Waveform struct {
	config  WaveformConfig
	advance float64
	time    float64
	scratch []float32
}

// NewWaveform mirrors ma_waveform_init.
func NewWaveform(config WaveformConfig) (*Waveform, error) {
	if config.Channels == 0 || config.SampleRate == 0 {
		return nil, ErrInvalidArgs
	}
	if config.Format == FormatUnknown {
		return nil, ErrInvalidArgs
	}
	w := &Waveform{config: config}
	w.updateAdvance()
	return w, nil
}

func (w *Waveform) updateAdvance() {
	w.advance = w.config.Frequency / float64(w.config.SampleRate)
}

// SetAmplitude mirrors ma_waveform_set_amplitude.
func (w *Waveform) SetAmplitude(amplitude float64) error {
	w.config.Amplitude = amplitude
	return nil
}

// SetFrequency mirrors ma_waveform_set_frequency.
func (w *Waveform) SetFrequency(frequency float64) error {
	w.config.Frequency = frequency
	w.updateAdvance()
	return nil
}

// SetType mirrors ma_waveform_set_type.
func (w *Waveform) SetType(typ WaveformType) error {
	w.config.Type = typ
	return nil
}

// SetSampleRate mirrors ma_waveform_set_sample_rate.
func (w *Waveform) SetSampleRate(sampleRate uint32) error {
	if sampleRate == 0 {
		return ErrInvalidArgs
	}
	w.config.SampleRate = sampleRate
	w.updateAdvance()
	return nil
}

func (w *Waveform) sample() float32 {
	t := w.time
	f := t - math.Floor(t)
	var r float64
	switch w.config.Type {
	case WaveformTypeSine:
		r = math.Sin(2 * math.Pi * t)
	case WaveformTypeSquare:
		if f < 0.5 {
			r = 1
		} else {
			r = -1
		}
	case WaveformTypeTriangle:
		r = 2*math.Abs(2*(f-0.5)) - 1
	case WaveformTypeSawtooth:
		r = 2 * (f - 0.5)
	}
	return float32(r * w.config.Amplitude)
}

// Read implements DataSource. It writes frameCount frames in the configured
// format, with the same value replicated across all channels.
func (w *Waveform) Read(framesOut []byte, frameCount uint64) (uint64, error) {
	ch := int(w.config.Channels)
	n := int(frameCount)

	if framesOut == nil {
		w.time += w.advance * float64(frameCount)
		return frameCount, nil
	}

	if cap(w.scratch) < n*ch {
		w.scratch = make([]float32, n*ch)
	}
	buf := w.scratch[:n*ch]
	for i := 0; i < n; i++ {
		s := w.sample()
		w.time += w.advance
		for c := 0; c < ch; c++ {
			buf[i*ch+c] = s
		}
	}

	if w.config.Format == FormatF32 {
		copy(bytesToF32(framesOut), buf)
		return frameCount, nil
	}
	if err := PCMConvert(framesOut, w.config.Format, f32ToBytes(buf), FormatF32, uint64(n*ch), DitherModeNone); err != nil {
		return 0, err
	}
	return frameCount, nil
}

// Seek implements DataSource, mirroring ma_waveform_seek_to_pcm_frame.
func (w *Waveform) Seek(frameIndex uint64) error {
	w.time = w.advance * float64(frameIndex)
	return nil
}

// DataFormat implements DataSource.
func (w *Waveform) DataFormat() (Format, uint32, uint32, []Channel, error) {
	return w.config.Format, w.config.Channels, w.config.SampleRate, nil, nil
}

// Cursor implements DataSource.
func (w *Waveform) Cursor() (uint64, error) {
	if w.advance == 0 {
		return 0, nil
	}
	return uint64(w.time / w.advance), nil
}

// Length implements DataSource. Waveforms are endless.
func (w *Waveform) Length() (uint64, error) {
	return 0, ErrNotImplemented
}
