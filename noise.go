package gominiaudio

// NoiseType mirrors ma_noise_type.
type NoiseType uint32

const (
	NoiseTypeWhite    NoiseType = 0
	NoiseTypePink     NoiseType = 1
	NoiseTypeBrownian NoiseType = 2
)

// NoiseConfig mirrors ma_noise_config.
type NoiseConfig struct {
	Format            Format
	Channels          uint32
	Type              NoiseType
	Seed              int32
	Amplitude         float64
	DuplicateChannels bool // When true all channels get the same value per frame; otherwise each channel gets independent noise.
}

// NoiseConfigInit mirrors ma_noise_config_init.
func NoiseConfigInit(format Format, channels uint32, typ NoiseType, seed int32, amplitude float64) NoiseConfig {
	return NoiseConfig{Format: format, Channels: channels, Type: typ, Seed: seed, Amplitude: amplitude}
}

const pinkNoiseBinCount = 16

// Noise mirrors ma_noise: an endless noise generator implementing DataSource.
type Noise struct {
	config NoiseConfig
	rng    lcg

	// Pink noise state (Voss-McCartney), per channel.
	pinkBin     [][]float64
	pinkAccum   []float64
	pinkCounter []uint32

	// Brownian noise state, per channel.
	brownianAccum []float64

	cursor  uint64
	scratch []float32
}

// NewNoise mirrors ma_noise_init.
func NewNoise(config NoiseConfig) (*Noise, error) {
	if config.Channels == 0 || config.Channels > MaxChannels {
		return nil, ErrInvalidArgs
	}
	if config.Format == FormatUnknown {
		return nil, ErrInvalidArgs
	}
	n := &Noise{config: config}
	seed := config.Seed
	if seed == 0 {
		seed = 4321
	}
	n.rng = lcg{state: seed}

	ch := int(config.Channels)
	n.pinkBin = make([][]float64, ch)
	for i := range n.pinkBin {
		n.pinkBin[i] = make([]float64, pinkNoiseBinCount)
	}
	n.pinkAccum = make([]float64, ch)
	n.pinkCounter = make([]uint32, ch)
	for i := range n.pinkCounter {
		n.pinkCounter[i] = 1
	}
	n.brownianAccum = make([]float64, ch)
	return n, nil
}

// SetAmplitude mirrors ma_noise_set_amplitude.
func (n *Noise) SetAmplitude(amplitude float64) error {
	n.config.Amplitude = amplitude
	return nil
}

// SetSeed mirrors ma_noise_set_seed.
func (n *Noise) SetSeed(seed int32) error {
	if seed == 0 {
		seed = 4321
	}
	n.rng.state = seed
	return nil
}

// SetType mirrors ma_noise_set_type.
func (n *Noise) SetType(typ NoiseType) error {
	n.config.Type = typ
	return nil
}

func (n *Noise) white() float64 {
	// Random in [-1, 1].
	return float64(n.rng.randS32())/float64(0x7FFFFFFF)*2 - 1
}

func (n *Noise) sampleWhite(_ int) float64 {
	return n.white() * n.config.Amplitude
}

// samplePink generates pink noise using the Voss-McCartney algorithm: one of
// pinkNoiseBinCount white noise rows is updated per sample based on the
// trailing zero count of a counter, and the sum approximates a 1/f spectrum.
func (n *Noise) samplePink(ch int) float64 {
	counter := n.pinkCounter[ch]
	// Index of the bin to update = number of trailing zeros.
	bin := 0
	c := counter
	for c&1 == 0 && bin < pinkNoiseBinCount-1 {
		bin++
		c >>= 1
	}
	old := n.pinkBin[ch][bin]
	newV := n.white()
	n.pinkBin[ch][bin] = newV
	n.pinkAccum[ch] += newV - old
	n.pinkCounter[ch]++
	if n.pinkCounter[ch] == 0 {
		n.pinkCounter[ch] = 1
	}
	// Add a white component, normalize and apply gain compensation to bring
	// the RMS close to white noise at the same amplitude. The result is
	// clamped so the output never exceeds the configured amplitude.
	v := (n.pinkAccum[ch] + n.white()) / float64(pinkNoiseBinCount+1) * 2
	if v > 1 {
		v = 1
	} else if v < -1 {
		v = -1
	}
	return v * n.config.Amplitude
}

// sampleBrownian integrates white noise with a decay to keep it bounded.
func (n *Noise) sampleBrownian(ch int) float64 {
	v := (n.brownianAccum[ch] + n.white()) / 1.005
	n.brownianAccum[ch] = v
	v = v / 20
	if v > 1 {
		v = 1
	} else if v < -1 {
		v = -1
	}
	return v * n.config.Amplitude
}

// Read implements DataSource.
func (n *Noise) Read(framesOut []byte, frameCount uint64) (uint64, error) {
	ch := int(n.config.Channels)
	cnt := int(frameCount)

	sample := n.sampleWhite
	switch n.config.Type {
	case NoiseTypePink:
		sample = n.samplePink
	case NoiseTypeBrownian:
		sample = n.sampleBrownian
	}

	if framesOut == nil {
		for i := 0; i < cnt; i++ {
			if n.config.DuplicateChannels {
				sample(0)
			} else {
				for c := 0; c < ch; c++ {
					sample(c)
				}
			}
		}
		n.cursor += frameCount
		return frameCount, nil
	}

	if cap(n.scratch) < cnt*ch {
		n.scratch = make([]float32, cnt*ch)
	}
	buf := n.scratch[:cnt*ch]
	for i := 0; i < cnt; i++ {
		if n.config.DuplicateChannels {
			s := float32(sample(0))
			for c := 0; c < ch; c++ {
				buf[i*ch+c] = s
			}
		} else {
			for c := 0; c < ch; c++ {
				buf[i*ch+c] = float32(sample(c))
			}
		}
	}
	n.cursor += frameCount

	if n.config.Format == FormatF32 {
		copy(bytesToF32(framesOut), buf)
		return frameCount, nil
	}
	if err := PCMConvert(framesOut, n.config.Format, f32ToBytes(buf), FormatF32, uint64(cnt*ch), DitherModeNone); err != nil {
		return 0, err
	}
	return frameCount, nil
}

// Seek implements DataSource. Noise is stateless with respect to position,
// so seeking only moves the cursor.
func (n *Noise) Seek(frameIndex uint64) error {
	n.cursor = frameIndex
	return nil
}

// DataFormat implements DataSource.
func (n *Noise) DataFormat() (Format, uint32, uint32, []Channel, error) {
	return n.config.Format, n.config.Channels, 0, nil, nil
}

// Cursor implements DataSource.
func (n *Noise) Cursor() (uint64, error) { return n.cursor, nil }

// Length implements DataSource. Noise is endless.
func (n *Noise) Length() (uint64, error) { return 0, ErrNotImplemented }
