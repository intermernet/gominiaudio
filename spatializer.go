package gominiaudio

import (
	"math"
	"sync"
)

// Vec3 mirrors ma_vec3f.
type Vec3 struct {
	X, Y, Z float32
}

func (v Vec3) sub(o Vec3) Vec3    { return Vec3{v.X - o.X, v.Y - o.Y, v.Z - o.Z} }
func (v Vec3) dot(o Vec3) float32 { return v.X*o.X + v.Y*o.Y + v.Z*o.Z }
func (v Vec3) len() float32       { return float32(math.Sqrt(float64(v.dot(v)))) }
func (v Vec3) mul(s float32) Vec3 { return Vec3{v.X * s, v.Y * s, v.Z * s} }
func (v Vec3) neg() Vec3          { return Vec3{-v.X, -v.Y, -v.Z} }
func (v Vec3) cross(o Vec3) Vec3 {
	return Vec3{
		v.Y*o.Z - v.Z*o.Y,
		v.Z*o.X - v.X*o.Z,
		v.X*o.Y - v.Y*o.X,
	}
}
func (v Vec3) normalize() Vec3 {
	l := v.len()
	if l == 0 {
		return Vec3{}
	}
	return v.mul(1 / l)
}

/**************************************************************************
Spatializer listener
**************************************************************************/

// SpatializerListenerConfig mirrors ma_spatializer_listener_config.
type SpatializerListenerConfig struct {
	ChannelsOut             uint32
	ChannelMapOut           []Channel
	HandedNess              Handedness
	ConeInnerAngleInRadians float32
	ConeOuterAngleInRadians float32
	ConeOuterGain           float32
	SpeedOfSound            float32
	WorldUp                 Vec3
}

// Handedness mirrors ma_handedness.
type Handedness uint32

const (
	HandednessRight Handedness = 0
	HandednessLeft  Handedness = 1
)

// SpatializerListenerConfigInit mirrors ma_spatializer_listener_config_init.
func SpatializerListenerConfigInit(channelsOut uint32) SpatializerListenerConfig {
	return SpatializerListenerConfig{
		ChannelsOut:             channelsOut,
		ConeInnerAngleInRadians: 2 * math.Pi,
		ConeOuterAngleInRadians: 2 * math.Pi,
		ConeOuterGain:           0,
		SpeedOfSound:            343.3,
		WorldUp:                 Vec3{0, 1, 0},
	}
}

// SpatializerListener mirrors ma_spatializer_listener.
type SpatializerListener struct {
	mu        sync.Mutex
	config    SpatializerListenerConfig
	position  Vec3
	direction Vec3
	velocity  Vec3
	isEnabled bool
}

// NewSpatializerListener mirrors ma_spatializer_listener_init.
func NewSpatializerListener(config SpatializerListenerConfig) (*SpatializerListener, error) {
	if config.ChannelsOut == 0 {
		return nil, ErrInvalidArgs
	}
	return &SpatializerListener{
		config:    config,
		direction: Vec3{0, 0, -1},
		isEnabled: true,
	}, nil
}

// SetPosition mirrors ma_spatializer_listener_set_position.
func (l *SpatializerListener) SetPosition(x, y, z float32) {
	l.mu.Lock()
	l.position = Vec3{x, y, z}
	l.mu.Unlock()
}

// Position mirrors ma_spatializer_listener_get_position.
func (l *SpatializerListener) Position() Vec3 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.position
}

// SetDirection mirrors ma_spatializer_listener_set_direction.
func (l *SpatializerListener) SetDirection(x, y, z float32) {
	l.mu.Lock()
	l.direction = Vec3{x, y, z}
	l.mu.Unlock()
}

// Direction mirrors ma_spatializer_listener_get_direction.
func (l *SpatializerListener) Direction() Vec3 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.direction
}

// SetVelocity mirrors ma_spatializer_listener_set_velocity.
func (l *SpatializerListener) SetVelocity(x, y, z float32) {
	l.mu.Lock()
	l.velocity = Vec3{x, y, z}
	l.mu.Unlock()
}

// Velocity mirrors ma_spatializer_listener_get_velocity.
func (l *SpatializerListener) Velocity() Vec3 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.velocity
}

// SetSpeedOfSound mirrors ma_spatializer_listener_set_speed_of_sound.
func (l *SpatializerListener) SetSpeedOfSound(v float32) {
	l.mu.Lock()
	l.config.SpeedOfSound = v
	l.mu.Unlock()
}

// SpeedOfSound mirrors ma_spatializer_listener_get_speed_of_sound.
func (l *SpatializerListener) SpeedOfSound() float32 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.config.SpeedOfSound
}

// SetWorldUp mirrors ma_spatializer_listener_set_world_up.
func (l *SpatializerListener) SetWorldUp(x, y, z float32) {
	l.mu.Lock()
	l.config.WorldUp = Vec3{x, y, z}
	l.mu.Unlock()
}

// WorldUp mirrors ma_spatializer_listener_get_world_up.
func (l *SpatializerListener) WorldUp() Vec3 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.config.WorldUp
}

// SetCone mirrors ma_spatializer_listener_set_cone.
func (l *SpatializerListener) SetCone(innerAngleInRadians, outerAngleInRadians, outerGain float32) {
	l.mu.Lock()
	l.config.ConeInnerAngleInRadians = innerAngleInRadians
	l.config.ConeOuterAngleInRadians = outerAngleInRadians
	l.config.ConeOuterGain = outerGain
	l.mu.Unlock()
}

// Cone mirrors ma_spatializer_listener_get_cone.
func (l *SpatializerListener) Cone() (innerAngleInRadians, outerAngleInRadians, outerGain float32) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.config.ConeInnerAngleInRadians, l.config.ConeOuterAngleInRadians, l.config.ConeOuterGain
}

// SetEnabled mirrors ma_spatializer_listener_set_enabled.
func (l *SpatializerListener) SetEnabled(enabled bool) {
	l.mu.Lock()
	l.isEnabled = enabled
	l.mu.Unlock()
}

// IsEnabled mirrors ma_spatializer_listener_is_enabled.
func (l *SpatializerListener) IsEnabled() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.isEnabled
}

/**************************************************************************
Spatializer
**************************************************************************/

// SpatializerConfig mirrors ma_spatializer_config.
type SpatializerConfig struct {
	ChannelsIn                   uint32
	ChannelsOut                  uint32
	ChannelMapIn                 []Channel
	AttenuationModel             AttenuationModel
	Positioning                  Positioning
	Handedness                   Handedness
	MinGain                      float32
	MaxGain                      float32
	MinDistance                  float32
	MaxDistance                  float32
	Rolloff                      float32
	ConeInnerAngleInRadians      float32
	ConeOuterAngleInRadians      float32
	ConeOuterGain                float32
	DopplerFactor                float32
	DirectionalAttenuationFactor float32
	MinSpatializationChannelGain float32
	GainSmoothTimeInFrames       uint32
}

// SpatializerConfigInit mirrors ma_spatializer_config_init.
func SpatializerConfigInit(channelsIn, channelsOut uint32) SpatializerConfig {
	return SpatializerConfig{
		ChannelsIn:                   channelsIn,
		ChannelsOut:                  channelsOut,
		AttenuationModel:             AttenuationModelInverse,
		Positioning:                  PositioningAbsolute,
		MinGain:                      0,
		MaxGain:                      1,
		MinDistance:                  1,
		MaxDistance:                  float32(math.MaxFloat32),
		Rolloff:                      1,
		ConeInnerAngleInRadians:      2 * math.Pi,
		ConeOuterAngleInRadians:      2 * math.Pi,
		ConeOuterGain:                0,
		DopplerFactor:                1,
		DirectionalAttenuationFactor: 1,
		MinSpatializationChannelGain: 0.2,
		GainSmoothTimeInFrames:       360, // 7.5ms @ 48kHz, matching miniaudio's engine default.
	}
}

// Spatializer mirrors ma_spatializer: positions a sound in 3D space relative
// to a listener, applying distance attenuation, cone attenuation, channel
// panning gains and doppler pitch.
type Spatializer struct {
	mu     sync.Mutex
	config SpatializerConfig

	position  Vec3
	direction Vec3
	velocity  Vec3

	dopplerPitch float32
	gainer       *Gainer
	firstProcess bool

	newChannelGains []float32
	appliedGains    []float32 // Gains last handed to the gainer; skips SetGains churn when unchanged.
	scratchOut      []float32

	// Per-output-channel speaker geometry, precomputed at init so Process
	// performs no channel map construction or position lookups.
	speakerDirs []Vec3
	spatialCh   []bool

	channelConverter *ChannelConverter // For the non-spatialized (relative, no attenuation) path and initial mapping.
}

// NewSpatializer mirrors ma_spatializer_init.
func NewSpatializer(config SpatializerConfig) (*Spatializer, error) {
	if config.ChannelsIn == 0 || config.ChannelsOut == 0 {
		return nil, ErrInvalidArgs
	}
	gainer, err := NewGainer(GainerConfigInit(config.ChannelsOut, config.GainSmoothTimeInFrames))
	if err != nil {
		return nil, err
	}
	ccCfg := ChannelConverterConfigInit(FormatF32, config.ChannelsIn, config.ChannelMapIn, config.ChannelsOut, nil, ChannelMixModeRectangular)
	cc, err := NewChannelConverter(ccCfg)
	if err != nil {
		return nil, err
	}
	s := &Spatializer{
		config:           config,
		direction:        Vec3{0, 0, -1},
		dopplerPitch:     1,
		gainer:           gainer,
		firstProcess:     true,
		newChannelGains:  make([]float32, config.ChannelsOut),
		appliedGains:     make([]float32, config.ChannelsOut),
		speakerDirs:      make([]Vec3, config.ChannelsOut),
		spatialCh:        make([]bool, config.ChannelsOut),
		channelConverter: cc,
	}
	channelMap := ChannelMapInitStandard(StandardChannelMapDefault, config.ChannelsOut)
	for i, pos := range channelMap {
		s.speakerDirs[i] = channelPositionDirection(pos)
		s.spatialCh[i] = isSpatialChannelPosition(pos)
	}
	for i := range s.appliedGains {
		s.appliedGains[i] = 1 // Matches the gainer's initial state.
	}
	return s, nil
}

// SetPosition mirrors ma_spatializer_set_position.
func (s *Spatializer) SetPosition(x, y, z float32) {
	s.mu.Lock()
	s.position = Vec3{x, y, z}
	s.mu.Unlock()
}

// Position mirrors ma_spatializer_get_position.
func (s *Spatializer) Position() Vec3 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.position
}

// SetDirection mirrors ma_spatializer_set_direction.
func (s *Spatializer) SetDirection(x, y, z float32) {
	s.mu.Lock()
	s.direction = Vec3{x, y, z}
	s.mu.Unlock()
}

// Direction mirrors ma_spatializer_get_direction.
func (s *Spatializer) Direction() Vec3 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.direction
}

// SetVelocity mirrors ma_spatializer_set_velocity.
func (s *Spatializer) SetVelocity(x, y, z float32) {
	s.mu.Lock()
	s.velocity = Vec3{x, y, z}
	s.mu.Unlock()
}

// Velocity mirrors ma_spatializer_get_velocity.
func (s *Spatializer) Velocity() Vec3 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.velocity
}

// Setters/getters mirroring the ma_spatializer_set_*/get_* API.

func (s *Spatializer) SetAttenuationModel(m AttenuationModel) {
	s.mu.Lock()
	s.config.AttenuationModel = m
	s.mu.Unlock()
}
func (s *Spatializer) AttenuationModel() AttenuationModel {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config.AttenuationModel
}
func (s *Spatializer) SetPositioning(p Positioning) {
	s.mu.Lock()
	s.config.Positioning = p
	s.mu.Unlock()
}
func (s *Spatializer) Positioning() Positioning {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config.Positioning
}
func (s *Spatializer) SetRolloff(v float32)     { s.mu.Lock(); s.config.Rolloff = v; s.mu.Unlock() }
func (s *Spatializer) Rolloff() float32         { s.mu.Lock(); defer s.mu.Unlock(); return s.config.Rolloff }
func (s *Spatializer) SetMinGain(v float32)     { s.mu.Lock(); s.config.MinGain = v; s.mu.Unlock() }
func (s *Spatializer) MinGain() float32         { s.mu.Lock(); defer s.mu.Unlock(); return s.config.MinGain }
func (s *Spatializer) SetMaxGain(v float32)     { s.mu.Lock(); s.config.MaxGain = v; s.mu.Unlock() }
func (s *Spatializer) MaxGain() float32         { s.mu.Lock(); defer s.mu.Unlock(); return s.config.MaxGain }
func (s *Spatializer) SetMinDistance(v float32) { s.mu.Lock(); s.config.MinDistance = v; s.mu.Unlock() }
func (s *Spatializer) MinDistance() float32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config.MinDistance
}
func (s *Spatializer) SetMaxDistance(v float32) { s.mu.Lock(); s.config.MaxDistance = v; s.mu.Unlock() }
func (s *Spatializer) MaxDistance() float32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config.MaxDistance
}
func (s *Spatializer) SetDopplerFactor(v float32) {
	s.mu.Lock()
	s.config.DopplerFactor = v
	s.mu.Unlock()
}
func (s *Spatializer) DopplerFactor() float32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config.DopplerFactor
}
func (s *Spatializer) SetDirectionalAttenuationFactor(v float32) {
	s.mu.Lock()
	s.config.DirectionalAttenuationFactor = v
	s.mu.Unlock()
}
func (s *Spatializer) DirectionalAttenuationFactor() float32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config.DirectionalAttenuationFactor
}

// SetCone mirrors ma_spatializer_set_cone.
func (s *Spatializer) SetCone(innerAngleInRadians, outerAngleInRadians, outerGain float32) {
	s.mu.Lock()
	s.config.ConeInnerAngleInRadians = innerAngleInRadians
	s.config.ConeOuterAngleInRadians = outerAngleInRadians
	s.config.ConeOuterGain = outerGain
	s.mu.Unlock()
}

// Cone mirrors ma_spatializer_get_cone.
func (s *Spatializer) Cone() (innerAngleInRadians, outerAngleInRadians, outerGain float32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config.ConeInnerAngleInRadians, s.config.ConeOuterAngleInRadians, s.config.ConeOuterGain
}

// InputChannels mirrors ma_spatializer_get_input_channels.
func (s *Spatializer) InputChannels() uint32 { return s.config.ChannelsIn }

// OutputChannels mirrors ma_spatializer_get_output_channels.
func (s *Spatializer) OutputChannels() uint32 { return s.config.ChannelsOut }

// DopplerPitch returns the pitch multiplier computed by the last Process
// call, to be applied by the caller's resampler.
func (s *Spatializer) DopplerPitch() float32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dopplerPitch
}

// RelativePositionAndDirection mirrors
// ma_spatializer_get_relative_position_and_direction.
func (s *Spatializer) RelativePositionAndDirection(listener *SpatializerListener) (relativePos, relativeDir Vec3) {
	s.mu.Lock()
	position := s.position
	direction := s.direction
	positioning := s.config.Positioning
	s.mu.Unlock()

	if positioning == PositioningRelative || listener == nil {
		return position, direction
	}

	listener.mu.Lock()
	lp := listener.position
	ld := listener.direction.normalize()
	up := listener.config.WorldUp.normalize()
	listener.mu.Unlock()

	// Build the listener's basis (right-handed by default; forward is -Z).
	axisZ := ld.neg()
	axisX := up.cross(axisZ).normalize()
	if axisX.len() == 0 {
		axisX = Vec3{1, 0, 0}
	}
	axisY := axisZ.cross(axisX)

	rel := position.sub(lp)
	relativePos = Vec3{rel.dot(axisX), rel.dot(axisY), rel.dot(axisZ)}
	relativeDir = Vec3{direction.dot(axisX), direction.dot(axisY), direction.dot(axisZ)}
	return relativePos, relativeDir
}

// calculateAttenuation computes gain from distance per the attenuation model.
func calculateAttenuation(model AttenuationModel, distance, minDistance, maxDistance, rolloff float32) float32 {
	d := distance
	if d < minDistance {
		d = minDistance
	}
	if d > maxDistance {
		d = maxDistance
	}
	switch model {
	case AttenuationModelInverse:
		denom := minDistance + rolloff*(d-minDistance)
		if denom <= 0 {
			return 1
		}
		return minDistance / denom
	case AttenuationModelLinear:
		if maxDistance <= minDistance {
			return 1
		}
		return 1 - rolloff*(d-minDistance)/(maxDistance-minDistance)
	case AttenuationModelExponential:
		if minDistance <= 0 {
			return 1
		}
		return float32(math.Pow(float64(d/minDistance), float64(-rolloff)))
	default:
		return 1
	}
}

// calculateAngularGain computes the cone attenuation for a direction.
func calculateAngularGain(coneDirection, toTarget Vec3, innerAngle, outerAngle, outerGain float32) float32 {
	if innerAngle >= 2*math.Pi {
		return 1
	}
	cd := coneDirection.normalize()
	tt := toTarget.normalize()
	if cd.len() == 0 || tt.len() == 0 {
		return 1
	}
	cosAngle := cd.dot(tt)
	angle := float32(math.Acos(float64(clampF32(cosAngle, -1, 1))))
	halfInner := innerAngle * 0.5
	halfOuter := outerAngle * 0.5
	if angle <= halfInner {
		return 1
	}
	if angle >= halfOuter {
		return outerGain
	}
	if halfOuter <= halfInner {
		return outerGain
	}
	a := (angle - halfInner) / (halfOuter - halfInner)
	return 1 + (outerGain-1)*a
}

func clampF32(x, lo, hi float32) float32 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

// Process mirrors ma_spatializer_process_pcm_frames: spatializes
// interleaved f32 input frames into output frames.
func (s *Spatializer) Process(listener *SpatializerListener, dst, src []float32, frameCount uint64) error {
	// Take a single snapshot of all mutable state under one lock acquisition.
	// This avoids holding the lock across any DSP work and eliminates the
	// re-acquisitions that previously occurred inside RelativePositionAndDirection.
	s.mu.Lock()
	cfg := s.config
	position := s.position
	direction := s.direction
	velocity := s.velocity
	firstProcess := s.firstProcess
	if firstProcess {
		s.firstProcess = false
	}
	s.mu.Unlock()

	cout := int(cfg.ChannelsOut)
	n := int(frameCount)
	if len(dst) < n*cout {
		return ErrInvalidArgs
	}

	if cfg.AttenuationModel == AttenuationModelNone || listener == nil || !listener.IsEnabled() {
		// No spatialization: plain channel conversion.
		if err := s.channelConverter.ProcessF32(dst, src, frameCount); err != nil {
			return err
		}
		s.mu.Lock()
		s.dopplerPitch = 1
		s.mu.Unlock()
		return nil
	}

	// Compute relative position using the already-captured source state and
	// a single listener snapshot (no re-acquisition of s.mu).
	relativePos, relativeDir := s.relativePositionAndDirectionFromSnapshot(
		position, direction, cfg.Positioning, listener)
	distance := relativePos.len()

	// Distance attenuation.
	gain := calculateAttenuation(cfg.AttenuationModel, distance, cfg.MinDistance, cfg.MaxDistance, cfg.Rolloff)

	// Source cone attenuation (does the source point at the listener?).
	if distance > 0.001 {
		coneGain := calculateAngularGain(relativeDir, relativePos.neg(), cfg.ConeInnerAngleInRadians, cfg.ConeOuterAngleInRadians, cfg.ConeOuterGain)
		if cfg.DirectionalAttenuationFactor != 1 {
			coneGain = 1 + (coneGain-1)*cfg.DirectionalAttenuationFactor
		}
		gain *= coneGain

		// Listener cone attenuation (is the listener facing the source?).
		linner, louter, louterGain := listener.Cone()
		listenerConeGain := calculateAngularGain(Vec3{0, 0, -1}, relativePos, linner, louter, louterGain)
		gain *= listenerConeGain
	}

	gain = clampF32(gain, cfg.MinGain, cfg.MaxGain)

	// Compute per-output-channel panning gains from the direction.
	s.computeChannelGains(relativePos, gain, cfg)

	// Convert channels, then apply the smoothed per-channel gains. Only
	// hand new gains to the gainer when they actually changed: SetGains
	// restarts the smoothing ramp, so calling it every block would keep the
	// gainer ramping (and doing per-sample interpolation) forever.
	if err := s.channelConverter.ProcessF32(dst, src, frameCount); err != nil {
		return err
	}
	gainsChanged := firstProcess
	if !gainsChanged {
		for i, g := range s.newChannelGains {
			if g != s.appliedGains[i] {
				gainsChanged = true
				break
			}
		}
	}
	if gainsChanged {
		_ = s.gainer.SetGains(s.newChannelGains)
		copy(s.appliedGains, s.newChannelGains)
		if firstProcess {
			// Apply the initial spatial gains immediately: a sound starting
			// far away must not ramp in from full volume.
			s.gainer.snapToTarget()
		}
	}
	if err := s.gainer.Process(dst, dst, frameCount); err != nil {
		return err
	}

	// Doppler — snapshot listener velocity under the listener lock only.
	dopplerPitch := float32(1)
	if cfg.DopplerFactor > 0 && distance > 0.001 {
		listener.mu.Lock()
		lv := listener.velocity
		speedOfSound := listener.config.SpeedOfSound
		listener.mu.Unlock()

		dopplerPitch = calculateDopplerPitch(relativePos, velocity, lv, speedOfSound, cfg.DopplerFactor)
	}
	s.mu.Lock()
	s.dopplerPitch = dopplerPitch
	s.mu.Unlock()
	return nil
}

// relativePositionAndDirectionFromSnapshot computes the relative
// position/direction from already-captured source state (position, direction,
// positioning) and the listener's current state (under a fresh listener lock).
// This avoids re-acquiring s.mu inside Process.
func (s *Spatializer) relativePositionAndDirectionFromSnapshot(
	position, direction Vec3, positioning Positioning,
	listener *SpatializerListener,
) (relativePos, relativeDir Vec3) {
	if positioning == PositioningRelative || listener == nil {
		return position, direction
	}

	listener.mu.Lock()
	lp := listener.position
	ld := listener.direction.normalize()
	up := listener.config.WorldUp.normalize()
	listener.mu.Unlock()

	axisZ := ld.neg()
	axisX := up.cross(axisZ).normalize()
	if axisX.len() == 0 {
		axisX = Vec3{1, 0, 0}
	}
	axisY := axisZ.cross(axisX)

	rel := position.sub(lp)
	relativePos = Vec3{rel.dot(axisX), rel.dot(axisY), rel.dot(axisZ)}
	relativeDir = Vec3{direction.dot(axisX), direction.dot(axisY), direction.dot(axisZ)}
	return relativePos, relativeDir
}

// computeChannelGains fills newChannelGains with per-channel gains derived
// from the sound's direction relative to the listener, mirroring
// miniaudio's simple speaker-dot-product panning. Speaker geometry is
// precomputed at init; this performs no allocations.
func (s *Spatializer) computeChannelGains(relativePos Vec3, gain float32, cfg SpatializerConfig) {
	dir := relativePos.normalize()
	minGainPerChannel := cfg.MinSpatializationChannelGain
	spatial := relativePos.len() > 0.001

	for i := range s.newChannelGains {
		var chGain float32 = 1
		if spatial && s.spatialCh[i] {
			d := (s.speakerDirs[i].dot(dir) + 1) * 0.5 // 0..1
			chGain = minGainPerChannel + (1-minGainPerChannel)*d
		}
		s.newChannelGains[i] = gain * chGain
	}
}

// channelPositionDirection returns the unit vector for a speaker position,
// with -Z forward, +X right, +Y up (right-handed).
func channelPositionDirection(p Channel) Vec3 {
	const s = 0.7071067811865476 // 1/sqrt(2)
	switch p {
	case ChannelFrontLeft:
		return Vec3{-s, 0, -s}
	case ChannelFrontRight:
		return Vec3{s, 0, -s}
	case ChannelFrontCenter, ChannelMono:
		return Vec3{0, 0, -1}
	case ChannelBackLeft:
		return Vec3{-s, 0, s}
	case ChannelBackRight:
		return Vec3{s, 0, s}
	case ChannelBackCenter:
		return Vec3{0, 0, 1}
	case ChannelSideLeft:
		return Vec3{-1, 0, 0}
	case ChannelSideRight:
		return Vec3{1, 0, 0}
	case ChannelFrontLeftCenter:
		return Vec3{-0.5, 0, -0.866}
	case ChannelFrontRightCenter:
		return Vec3{0.5, 0, -0.866}
	case ChannelTopCenter:
		return Vec3{0, 1, 0}
	case ChannelTopFrontLeft:
		return Vec3{-0.5774, 0.5774, -0.5774}
	case ChannelTopFrontCenter:
		return Vec3{0, s, -s}
	case ChannelTopFrontRight:
		return Vec3{0.5774, 0.5774, -0.5774}
	case ChannelTopBackLeft:
		return Vec3{-0.5774, 0.5774, 0.5774}
	case ChannelTopBackCenter:
		return Vec3{0, s, s}
	case ChannelTopBackRight:
		return Vec3{0.5774, 0.5774, 0.5774}
	default:
		return Vec3{0, 0, -1}
	}
}

// calculateDopplerPitch mirrors ma_doppler_pitch. relativePos points from
// the listener to the source (in listener space, so the listener is at the
// origin).
func calculateDopplerPitch(relativePos, sourceVelocity, listenerVelocity Vec3, speedOfSound, dopplerFactor float32) float32 {
	distance := relativePos.len()
	if distance == 0 || speedOfSound <= 0 {
		return 1
	}
	// Component of each velocity along the source-listener axis.
	vls := relativePos.dot(listenerVelocity) / distance
	vss := relativePos.dot(sourceVelocity) / distance

	maxSpeed := speedOfSound / dopplerFactor
	vls = clampF32(vls, -maxSpeed, maxSpeed)
	vss = clampF32(vss, -maxSpeed, maxSpeed)

	num := speedOfSound + vls*dopplerFactor
	den := speedOfSound + vss*dopplerFactor
	if den == 0 {
		return 1
	}
	return num / den
}
