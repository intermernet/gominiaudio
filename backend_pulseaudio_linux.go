//go:build linux && !android

package gominiaudio

import (
	"sync"
)

// PulseAudio backend. Talks the native protocol directly (see
// pa_protocol_linux.go), so it works against both PulseAudio and
// PipeWire's pulse-server without libpulse or cgo.
//
// Playback is driven by the server's REQUEST events: the daemon tells the
// client how many bytes it may send, and the feeder goroutine pulls exactly
// that much from the device callback and writes it inline on the socket.
// Capture is the mirror image, with PCM arriving as data packets.

type pulseContext struct {
	mu     sync.Mutex
	client *paClient
}

func (c *pulseContext) init(*ContextConfig) error {
	client, err := paConnect("gominiaudio")
	if err != nil {
		return err
	}
	c.client = client
	return nil
}

func (c *pulseContext) uninit() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		c.client.close()
		c.client = nil
	}
	return nil
}

func (c *pulseContext) backendID() Backend { return BackendPulseAudio }

func (c *pulseContext) enumerateDevices() ([]DeviceInfo, []DeviceInfo, error) {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return nil, nil, ErrDeviceNotInitialized
	}

	info, err := client.serverInfo()
	if err != nil {
		return nil, nil, err
	}
	sinks, err := client.sinks()
	if err != nil {
		return nil, nil, err
	}
	sources, err := client.sources()
	if err != nil {
		return nil, nil, err
	}

	playback := make([]DeviceInfo, 0, len(sinks))
	for _, s := range sinks {
		playback = append(playback, paDeviceInfo(s, s.Name == info.DefaultSink))
	}
	capture := make([]DeviceInfo, 0, len(sources))
	for _, s := range sources {
		capture = append(capture, paDeviceInfo(s, s.Name == info.DefaultSource))
	}
	return playback, capture, nil
}

// paDeviceInfo converts a server entry into a DeviceInfo.
func paDeviceInfo(e paDeviceEntry, isDefault bool) DeviceInfo {
	name := e.Description
	if name == "" {
		name = e.Name
	}
	return DeviceInfo{
		ID:        deviceIDFromString(e.Name),
		Name:      name,
		IsDefault: isDefault,
		Formats: []DeviceNativeDataFormat{{
			Format:     paFormatToMA(e.Format),
			Channels:   uint32(e.Channels),
			SampleRate: e.SampleRate,
		}},
	}
}

func (c *pulseContext) deviceInfo(deviceType DeviceType, id *DeviceID) (DeviceInfo, error) {
	playback, capture, err := c.enumerateDevices()
	if err != nil {
		return DeviceInfo{}, err
	}
	list := playback
	if deviceType == DeviceTypeCapture || deviceType == DeviceTypeLoopback {
		list = capture
	}
	if len(list) == 0 {
		return DeviceInfo{}, ErrNoDevice
	}
	if id == nil || id.IsZero() {
		for _, d := range list {
			if d.IsDefault {
				return d, nil
			}
		}
		return list[0], nil
	}
	want := id.String()
	for _, d := range list {
		if d.ID.String() == want {
			return d, nil
		}
	}
	return DeviceInfo{}, ErrNoDevice
}

// paFormatToMA maps a PulseAudio sample format onto the library's.
func paFormatToMA(f uint8) Format {
	switch f {
	case paSampleU8:
		return FormatU8
	case paSampleS16LE:
		return FormatS16
	case paSampleS24LE:
		return FormatS24
	case paSampleS32LE:
		return FormatS32
	case paSampleFloat32LE:
		return FormatF32
	}
	return FormatUnknown
}

// paFormatFromMA maps a library format onto PulseAudio's. Only
// little-endian formats are produced; every supported target is LE.
func paFormatFromMA(f Format) uint8 {
	switch f {
	case FormatU8:
		return paSampleU8
	case FormatS16:
		return paSampleS16LE
	case FormatS24:
		return paSampleS24LE
	case FormatS32:
		return paSampleS32LE
	case FormatF32:
		return paSampleFloat32LE
	}
	return paSampleInvalid
}

// paChannelFromMA maps a library channel position onto PulseAudio's.
func paChannelFromMA(c Channel) uint8 {
	switch c {
	case ChannelMono:
		return paChannelMono
	case ChannelFrontLeft:
		return paChannelFrontLeft
	case ChannelFrontRight:
		return paChannelFrontRight
	case ChannelFrontCenter:
		return paChannelFrontCenter
	case ChannelLFE:
		return paChannelLFE
	case ChannelBackLeft:
		return paChannelRearLeft
	case ChannelBackRight:
		return paChannelRearRight
	case ChannelBackCenter:
		return paChannelRearCenter
	case ChannelFrontLeftCenter:
		return paChannelFrontLeftOfCenter
	case ChannelFrontRightCenter:
		return paChannelFrontRightOfCentr
	case ChannelSideLeft:
		return paChannelSideLeft
	case ChannelSideRight:
		return paChannelSideRight
	case ChannelTopCenter:
		return paChannelTopCenter
	case ChannelTopFrontLeft:
		return paChannelTopFrontLeft
	case ChannelTopFrontCenter:
		return paChannelTopFrontCenter
	case ChannelTopFrontRight:
		return paChannelTopFrontRight
	case ChannelTopBackLeft:
		return paChannelTopRearLeft
	case ChannelTopBackCenter:
		return paChannelTopRearCenter
	case ChannelTopBackRight:
		return paChannelTopRearRight
	}
	return paChannelMono
}

// paChannelMapFor builds a PulseAudio channel map for a channel count.
func paChannelMapFor(channels uint32) []uint8 {
	std := ChannelMapInitStandard(StandardChannelMapDefault, channels)
	out := make([]uint8, 0, channels)
	for i := uint32(0); i < channels; i++ {
		if int(i) < len(std) {
			out = append(out, paChannelFromMA(std[i]))
		} else {
			out = append(out, paChannelAux0+uint8(i))
		}
	}
	return out
}

// paErrorToResult maps a PA_ERR_* code onto the library's error set.
func paErrorToResult(code uint32) error {
	switch code {
	case 1: // PA_ERR_ACCESS
		return ErrAccessDenied
	case 3: // PA_ERR_INVALID
		return ErrInvalidArgs
	case 4: // PA_ERR_EXIST
		return ErrAlreadyExists
	case 5: // PA_ERR_NOENTITY
		return ErrNoDevice
	case 6: // PA_ERR_CONNECTIONREFUSED
		return ErrConnectionRefused
	case 7: // PA_ERR_PROTOCOL
		return ErrBadProtocol
	case 8: // PA_ERR_TIMEOUT
		return ErrTimeout
	case 9: // PA_ERR_AUTHKEY
		return ErrAccessDenied
	case 11: // PA_ERR_CONNECTIONTERMINATED
		return ErrConnectionReset
	case 12: // PA_ERR_KILLED
		return ErrCancelled
	case 15: // PA_ERR_BADSTATE
		return ErrInvalidOperation
	case 17: // PA_ERR_VERSION
		return ErrBadProtocol
	case 18: // PA_ERR_TOOLARGE
		return ErrTooBig
	case 19, 23: // PA_ERR_NOTSUPPORTED, PA_ERR_NOTIMPLEMENTED
		return ErrNotImplemented
	case 25: // PA_ERR_IO
		return ErrIOError
	case 26: // PA_ERR_BUSY
		return ErrBusy
	}
	return ErrorGeneric
}

// paStream is one playback or record stream.
type paStream struct {
	client  *paClient
	device  *Device
	capture bool

	channel   uint32 // Server-assigned stream index.
	format    Format
	channels  uint32
	rate      uint32
	bpf       int    // Bytes per frame.
	period    uint32 // Frames per period.
	maxLength uint32

	mu      sync.Mutex
	running bool
	dead    bool

	// credit is how many bytes the server has said this client may send.
	// PulseAudio is credit based: the initial allowance arrives in the
	// CREATE_PLAYBACK_STREAM reply and every REQUEST adds to it. Credit
	// must never be dropped, or the stream stalls for good once the
	// server stops asking.
	creditMu sync.Mutex
	credit   int64

	// pending holds captured bytes waiting to be handed to the callback.
	pendingMu sync.Mutex
	pending   []byte

	// wakeCh signals the transfer goroutine. It has depth one and is only
	// ever a nudge, so the socket reader never blocks on it.
	wakeCh chan struct{}
	stopCh chan struct{}
	doneCh chan struct{}

	scratch []byte
}

// addCredit records an allowance from the server and wakes the feeder.
func (s *paStream) addCredit(n int64) {
	if n <= 0 {
		return
	}
	s.creditMu.Lock()
	s.credit += n
	s.creditMu.Unlock()
	s.wake()
}

// takeCredit claims up to max bytes of allowance, rounded down to whole
// frames. The remainder stays on the counter for the next write.
func (s *paStream) takeCredit(max int64) int64 {
	s.creditMu.Lock()
	defer s.creditMu.Unlock()
	n := s.credit
	if n > max {
		n = max
	}
	n -= n % int64(s.bpf)
	if n <= 0 {
		return 0
	}
	s.credit -= n
	return n
}

// wake nudges the transfer goroutine.
func (s *paStream) wake() {
	select {
	case s.wakeCh <- struct{}{}:
	default:
	}
}

// onRequest implements paStreamHandler.
func (s *paStream) onRequest(nbytes uint32) {
	s.addCredit(int64(nbytes))
}

// onData implements paStreamHandler.
func (s *paStream) onData(pcm []byte) {
	if len(pcm) == 0 {
		return
	}
	s.pendingMu.Lock()
	// Bound the backlog: if the consumer has fallen far behind, drop the
	// oldest audio rather than grow without limit. Staying ahead of the
	// server is the normal case, so this is a safety valve.
	limit := int(s.maxLength) * 2
	if limit > 0 && len(s.pending)+len(pcm) > limit {
		drop := len(s.pending) + len(pcm) - limit
		if drop > len(s.pending) {
			drop = len(s.pending)
		}
		s.pending = append(s.pending[:0], s.pending[drop:]...)
	}
	s.pending = append(s.pending, pcm...)
	s.pendingMu.Unlock()
	s.wake()
}

// onKilled implements paStreamHandler.
func (s *paStream) onKilled() {
	s.mu.Lock()
	s.dead = true
	s.mu.Unlock()
	if s.device != nil {
		s.device.notify(DeviceNotificationTypeStopped)
	}
}

// paOpenStream creates a stream on the server and returns it corked.
func paOpenStream(client *paClient, d *Device, cfg *DeviceConfig, capture bool) (*paStream, error) {
	format := cfg.Playback.Format
	channels := cfg.Playback.Channels
	deviceID := cfg.Playback.DeviceID
	if capture {
		format = cfg.Capture.Format
		channels = cfg.Capture.Channels
		deviceID = cfg.Capture.DeviceID
	}
	if format == FormatUnknown {
		format = DefaultFormat
	}
	if channels == 0 {
		channels = DefaultChannels
	}
	rate := cfg.SampleRate
	if rate == 0 {
		rate = DefaultSampleRate
	}
	paFormat := paFormatFromMA(format)
	if paFormat == paSampleInvalid {
		return nil, ErrFormatNotSupported
	}

	period := d.calculatePeriodSizeInFrames(rate)
	bpf := FrameSizeInBytes(format, channels)
	periods := cfg.Periods
	if periods == 0 {
		periods = DefaultPeriods
	}

	s := &paStream{
		client:   client,
		device:   d,
		capture:  capture,
		format:   format,
		channels: channels,
		rate:     rate,
		bpf:      bpf,
		period:   period,
		wakeCh:   make(chan struct{}, 1),
	}

	// Buffer attributes. tlength is the target fill level: the server
	// requests more whenever the buffer drops below it, so it sets the
	// latency. minreq keeps each request at one period so the callback is
	// driven at the cadence the caller asked for.
	periodBytes := uint32(int(period) * bpf)
	tlength := periodBytes * periods
	s.maxLength = tlength * 2

	var w paTagWriter
	command := paCommandCreatePlaybackStream
	if capture {
		command = paCommandCreateRecordStream
	}
	w.putU32(command)
	tag := client.allocTag()
	w.putU32(tag)

	w.putSampleSpec(paFormat, uint8(channels), rate)
	w.putChannelMap(paChannelMapFor(channels))
	w.putU32(paInvalidIndex) // Device index: select by name instead.
	if deviceID != nil && !deviceID.IsZero() {
		w.putString(deviceID.String())
	} else {
		w.putNullString() // Server default.
	}
	w.putU32(s.maxLength)
	w.putBool(true) // Start corked; start() uncorks.

	if capture {
		w.putU32(periodBytes) // fragsize
	} else {
		w.putU32(tlength)
		w.putU32(periodBytes) // prebuf: start after one period is buffered.
		w.putU32(periodBytes) // minreq
		w.putU32(paInvalidIndex)
		volumes := make([]uint32, channels)
		for i := range volumes {
			volumes[i] = paVolumeNorm
		}
		w.putCVolume(volumes)
	}

	// Protocol 12 flags, in order: no_remap, no_remix, fix_format,
	// fix_rate, fix_channels, no_move, variable_rate.
	w.putBool(false)
	w.putBool(false)
	w.putBool(false)
	w.putBool(false)
	w.putBool(false)
	w.putBool(false)
	w.putBool(false)

	// Protocol 13 additions.
	if capture {
		w.putBool(false) // peak_detect
		w.putBool(true)  // adjust_latency
		w.putProplist([][2]string{{"media.name", "gominiaudio capture"}})
		w.putU32(paInvalidIndex) // direct_on_input
	} else {
		w.putBool(false) // muted
		w.putBool(true)  // adjust_latency
		w.putProplist([][2]string{{"media.name", "gominiaudio playback"}})
	}

	reply, err := client.requestWithTag(tag, w.buf, false)
	if err != nil {
		return nil, err
	}

	r := &paTagReader{buf: reply}
	if s.channel, err = r.getU32(); err != nil {
		return nil, ErrBadProtocol
	}
	if _, err := r.getU32(); err != nil { // sink_input / source_output index.
		return nil, ErrBadProtocol
	}
	if !capture {
		// The server's opening allowance. This is the only credit that
		// arrives outside a REQUEST, and without it the stream never
		// starts: nothing is written, so nothing drains, so the server
		// never asks for more.
		missing, err := r.getU32()
		if err != nil {
			return nil, ErrBadProtocol
		}
		s.credit = int64(missing)
	}

	// Buffer metrics, sent since protocol 9.
	actualMaxLength, err := r.getU32()
	if err != nil {
		return nil, ErrBadProtocol
	}
	s.maxLength = actualMaxLength
	if capture {
		if _, err := r.getU32(); err != nil { // fragsize
			return nil, ErrBadProtocol
		}
	} else {
		if _, err := r.getU32(); err != nil { // tlength
			return nil, ErrBadProtocol
		}
		if _, err := r.getU32(); err != nil { // prebuf
			return nil, ErrBadProtocol
		}
		if _, err := r.getU32(); err != nil { // minreq
			return nil, ErrBadProtocol
		}
	}

	// Negotiated format, sent since protocol 12. The server may have
	// picked something other than we asked for.
	if client.version >= 12 {
		f, ch, sr, err := r.getSampleSpec()
		if err != nil {
			return nil, ErrBadProtocol
		}
		if got := paFormatToMA(f); got != FormatUnknown {
			s.format = got
		}
		if ch != 0 {
			s.channels = uint32(ch)
		}
		if sr != 0 {
			s.rate = sr
		}
		s.bpf = FrameSizeInBytes(s.format, s.channels)
	}

	client.registerStream(s.channel, s)
	return s, nil
}

// start uncorks the stream and spins up its transfer goroutine.
func (s *paStream) start() error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	if s.dead {
		s.mu.Unlock()
		return ErrDeviceNotInitialized
	}
	s.running = true
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})
	stop, done := s.stopCh, s.doneCh
	s.mu.Unlock()

	if err := s.cork(false); err != nil {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		return err
	}

	if s.capture {
		go s.captureLoop(stop, done)
	} else {
		go s.playbackLoop(stop, done)
		// Kick the feeder so it spends the opening allowance immediately
		// rather than waiting for the first REQUEST.
		s.wake()
	}
	return nil
}

// stop corks the stream and waits for the transfer goroutine to exit.
func (s *paStream) stop() error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = false
	stop, done := s.stopCh, s.doneCh
	dead := s.dead
	s.mu.Unlock()

	close(stop)
	<-done

	if dead {
		return nil
	}
	return s.cork(true)
}

// cork pauses or resumes the stream server-side.
func (s *paStream) cork(corked bool) error {
	command := paCommandCorkPlaybackStream
	if s.capture {
		command = paCommandCorkRecordStream
	}
	_, err := s.client.request(command, func(w *paTagWriter) {
		w.putU32(s.channel)
		w.putBool(corked)
	})
	return err
}

// playbackLoop writes audio for as long as the server grants credit.
func (s *paStream) playbackLoop(stop, done chan struct{}) {
	defer close(done)

	periodBytes := int(s.period) * s.bpf
	if cap(s.scratch) < periodBytes {
		s.scratch = make([]byte, periodBytes)
	}

	for {
		select {
		case <-stop:
			return
		case <-s.client.doneCh:
			return
		default:
		}

		n := s.takeCredit(int64(periodBytes))
		if n == 0 {
			// Out of credit: wait for the next REQUEST.
			select {
			case <-stop:
				return
			case <-s.client.doneCh:
				return
			case <-s.wakeCh:
			}
			continue
		}

		frames := uint32(n) / uint32(s.bpf)
		buf := s.scratch[:n]
		s.device.handlePlayback(buf, frames)

		if err := s.client.conn.sendData(s.channel, 0, paSeekRelative, buf); err != nil {
			return
		}
	}
}

// captureLoop forwards PCM arriving from the server.
func (s *paStream) captureLoop(stop, done chan struct{}) {
	defer close(done)

	periodBytes := int(s.period) * s.bpf
	if cap(s.scratch) < periodBytes {
		s.scratch = make([]byte, periodBytes)
	}

	for {
		select {
		case <-stop:
			return
		case <-s.client.doneCh:
			return
		default:
		}

		// Hand the callback fixed-size periods; the server's fragments do
		// not necessarily line up with the caller's period size.
		s.pendingMu.Lock()
		ready := len(s.pending) >= periodBytes
		if ready {
			copy(s.scratch[:periodBytes], s.pending[:periodBytes])
			s.pending = append(s.pending[:0], s.pending[periodBytes:]...)
		}
		s.pendingMu.Unlock()

		if !ready {
			select {
			case <-stop:
				return
			case <-s.client.doneCh:
				return
			case <-s.wakeCh:
			}
			continue
		}
		s.device.handleCapture(s.scratch[:periodBytes], s.period)
	}
}

// close destroys the stream server-side.
func (s *paStream) close() {
	s.stop()
	s.client.unregisterStream(s.channel)

	s.mu.Lock()
	dead := s.dead
	s.mu.Unlock()
	if dead {
		return
	}

	command := paCommandDeletePlaybackStream
	if s.capture {
		command = paCommandDeleteRecordStream
	}
	s.client.request(command, func(w *paTagWriter) {
		w.putU32(s.channel)
	})
}

// pulseDevice is the deviceBackend for PulseAudio.
type pulseDevice struct {
	playback *paStream
	capture  *paStream
}

func (c *pulseContext) newDevice(d *Device, config *DeviceConfig) (deviceBackend, error) {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return nil, ErrDeviceNotInitialized
	}

	dev := &pulseDevice{}

	if d.deviceType&DeviceTypePlayback != 0 {
		s, err := paOpenStream(client, d, config, false)
		if err != nil {
			return nil, err
		}
		dev.playback = s
		d.setInternalFormat(DeviceTypePlayback, s.format, s.channels, s.rate, nil,
			s.period, config.Periods, "PulseAudio Playback")
	}
	if d.deviceType&DeviceTypeCapture != 0 || d.deviceType == DeviceTypeLoopback {
		s, err := paOpenStream(client, d, config, true)
		if err != nil {
			if dev.playback != nil {
				dev.playback.close()
			}
			return nil, err
		}
		dev.capture = s
		d.setInternalFormat(DeviceTypeCapture, s.format, s.channels, s.rate, nil,
			s.period, config.Periods, "PulseAudio Capture")
	}
	return dev, nil
}

func (pd *pulseDevice) start() error {
	if pd.capture != nil {
		if err := pd.capture.start(); err != nil {
			return err
		}
	}
	if pd.playback != nil {
		if err := pd.playback.start(); err != nil {
			if pd.capture != nil {
				pd.capture.stop()
			}
			return err
		}
	}
	return nil
}

func (pd *pulseDevice) stop() error {
	var firstErr error
	if pd.playback != nil {
		if err := pd.playback.stop(); err != nil {
			firstErr = err
		}
	}
	if pd.capture != nil {
		if err := pd.capture.stop(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (pd *pulseDevice) uninit() error {
	if pd.playback != nil {
		pd.playback.close()
		pd.playback = nil
	}
	if pd.capture != nil {
		pd.capture.close()
		pd.capture = nil
	}
	return nil
}
