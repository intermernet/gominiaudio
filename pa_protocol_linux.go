//go:build linux && !android

package gominiaudio

import (
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// PulseAudio native protocol, implemented directly on the daemon's unix
// socket so the backend needs neither libpulse nor cgo.
//
// Every packet is a 20 byte descriptor followed by a payload:
//
//	u32 length     - payload length
//	u32 channel    - 0xFFFFFFFF for commands, else the stream index
//	u32 offset_hi  - seek offset, high word (data packets)
//	u32 offset_lo  - seek offset, low word (data packets)
//	u32 flags      - seek mode / shm flags (data packets)
//
// Command payloads are "tagstructs": a stream of tag-prefixed values, all
// integers big endian. Data packets carry raw PCM instead.
//
// Constants below follow PulseAudio's src/pulsecore/native-common.h,
// src/pulsecore/tagstruct.h, src/pulse/sample.h and src/pulse/channelmap.h.

// paProtocolVersion is the protocol version this client advertises.
//
// 13 is the oldest version carrying proplists and latency adjustment, and
// both PulseAudio and PipeWire's pulse-server accept anything >= 8. It is
// deliberately low: every later revision adds fields to the replies we must
// parse (device ports in v16, format_info in v21) without adding anything
// this backend needs, so negotiating 13 keeps the parser small and the wire
// format fixed regardless of which server is on the other end.
const paProtocolVersion = 13

// Descriptor constants.
const (
	paDescriptorSize    = 20
	paChannelCommand    = 0xFFFFFFFF // Descriptor channel for command packets.
	paProtoVersionMask  = 0x0000FFFF
	paProtoFlagSHM      = 0x80000000
	paNativeCookieBytes = 256
	paMaxPacketSize     = 4 * 1024 * 1024
)

// Seek modes for playback data packets.
const (
	paSeekRelative uint32 = 0
	paSeekAbsolute uint32 = 1
)

// Commands, by ordinal position in the PA_COMMAND_* enum.
const (
	paCommandError                uint32 = 0
	paCommandReply                uint32 = 2
	paCommandCreatePlaybackStream uint32 = 3
	paCommandDeletePlaybackStream uint32 = 4
	paCommandCreateRecordStream   uint32 = 5
	paCommandDeleteRecordStream   uint32 = 6
	paCommandAuth                 uint32 = 8
	paCommandSetClientName        uint32 = 9
	paCommandGetServerInfo        uint32 = 20
	paCommandGetSinkInfoList      uint32 = 22
	paCommandGetSourceInfoList    uint32 = 24
	paCommandCorkPlaybackStream   uint32 = 41
	paCommandFlushPlaybackStream  uint32 = 42
	paCommandGetRecordLatency     uint32 = 57
	paCommandCorkRecordStream     uint32 = 58
	paCommandFlushRecordStream    uint32 = 59
	paCommandPrebufPlaybackStream uint32 = 60
	paCommandRequest              uint32 = 61
	paCommandOverflow             uint32 = 62
	paCommandUnderflow            uint32 = 63
	paCommandPlaybackStreamKilled uint32 = 64
	paCommandRecordStreamKilled   uint32 = 65
	paCommandSubscribeEvent       uint32 = 66
)

// Tagstruct tags.
const (
	paTagString     byte = 't'
	paTagStringNull byte = 'N'
	paTagU32        byte = 'L'
	paTagU8         byte = 'B'
	paTagU64        byte = 'R'
	paTagS64        byte = 'r'
	paTagSampleSpec byte = 'a'
	paTagArbitrary  byte = 'x'
	paTagBoolTrue   byte = '1'
	paTagBoolFalse  byte = '0'
	paTagTimeval    byte = 'T'
	paTagUsec       byte = 'U'
	paTagChannelMap byte = 'm'
	paTagCVolume    byte = 'v'
	paTagProplist   byte = 'P'
	paTagVolume     byte = 'V'
	paTagFormatInfo byte = 'f'
)

// Sample formats (pa_sample_format_t).
const (
	paSampleU8        uint8 = 0
	paSampleALaw      uint8 = 1
	paSampleULaw      uint8 = 2
	paSampleS16LE     uint8 = 3
	paSampleS16BE     uint8 = 4
	paSampleFloat32LE uint8 = 5
	paSampleFloat32BE uint8 = 6
	paSampleS32LE     uint8 = 7
	paSampleS32BE     uint8 = 8
	paSampleS24LE     uint8 = 9
	paSampleS24BE     uint8 = 10
	paSampleS24_32LE  uint8 = 11
	paSampleS24_32BE  uint8 = 12
	paSampleInvalid   uint8 = 0xFF
)

// Channel positions (pa_channel_position_t).
const (
	paChannelMono              uint8 = 0
	paChannelFrontLeft         uint8 = 1
	paChannelFrontRight        uint8 = 2
	paChannelFrontCenter       uint8 = 3
	paChannelRearCenter        uint8 = 4
	paChannelRearLeft          uint8 = 5
	paChannelRearRight         uint8 = 6
	paChannelLFE               uint8 = 7
	paChannelFrontLeftOfCenter uint8 = 8
	paChannelFrontRightOfCentr uint8 = 9
	paChannelSideLeft          uint8 = 10
	paChannelSideRight         uint8 = 11
	paChannelAux0              uint8 = 12
	paChannelTopCenter         uint8 = 44
	paChannelTopFrontLeft      uint8 = 45
	paChannelTopFrontRight     uint8 = 46
	paChannelTopFrontCenter    uint8 = 47
	paChannelTopRearLeft       uint8 = 48
	paChannelTopRearRight      uint8 = 49
	paChannelTopRearCenter     uint8 = 50
)

// paVolumeNorm is PA_VOLUME_NORM: unattenuated playback volume.
const paVolumeNorm uint32 = 0x10000

// paInvalidIndex is PA_INVALID_INDEX.
const paInvalidIndex uint32 = 0xFFFFFFFF

var errPAShortRead = errors.New("pulse: truncated tagstruct")

// paTagWriter builds a tagstruct payload.
type paTagWriter struct {
	buf []byte
}

func (w *paTagWriter) putU8(v uint8) {
	w.buf = append(w.buf, paTagU8, v)
}

func (w *paTagWriter) putU32(v uint32) {
	w.buf = append(w.buf, paTagU32, 0, 0, 0, 0)
	binary.BigEndian.PutUint32(w.buf[len(w.buf)-4:], v)
}

func (w *paTagWriter) putU64(v uint64) {
	w.buf = append(w.buf, paTagU64, 0, 0, 0, 0, 0, 0, 0, 0)
	binary.BigEndian.PutUint64(w.buf[len(w.buf)-8:], v)
}

func (w *paTagWriter) putUsec(v uint64) {
	w.buf = append(w.buf, paTagUsec, 0, 0, 0, 0, 0, 0, 0, 0)
	binary.BigEndian.PutUint64(w.buf[len(w.buf)-8:], v)
}

// putString writes a NUL terminated string.
func (w *paTagWriter) putString(s string) {
	w.buf = append(w.buf, paTagString)
	w.buf = append(w.buf, s...)
	w.buf = append(w.buf, 0)
}

// putNullString writes the distinct "no string" value.
func (w *paTagWriter) putNullString() {
	w.buf = append(w.buf, paTagStringNull)
}

func (w *paTagWriter) putBool(b bool) {
	if b {
		w.buf = append(w.buf, paTagBoolTrue)
	} else {
		w.buf = append(w.buf, paTagBoolFalse)
	}
}

func (w *paTagWriter) putArbitrary(b []byte) {
	w.buf = append(w.buf, paTagArbitrary, 0, 0, 0, 0)
	binary.BigEndian.PutUint32(w.buf[len(w.buf)-4:], uint32(len(b)))
	w.buf = append(w.buf, b...)
}

func (w *paTagWriter) putSampleSpec(format, channels uint8, rate uint32) {
	w.buf = append(w.buf, paTagSampleSpec, format, channels, 0, 0, 0, 0)
	binary.BigEndian.PutUint32(w.buf[len(w.buf)-4:], rate)
}

func (w *paTagWriter) putChannelMap(positions []uint8) {
	w.buf = append(w.buf, paTagChannelMap, uint8(len(positions)))
	w.buf = append(w.buf, positions...)
}

func (w *paTagWriter) putCVolume(volumes []uint32) {
	w.buf = append(w.buf, paTagCVolume, uint8(len(volumes)))
	for _, v := range volumes {
		w.buf = append(w.buf, 0, 0, 0, 0)
		binary.BigEndian.PutUint32(w.buf[len(w.buf)-4:], v)
	}
}

// putProplist writes a property list. Values are stored NUL terminated to
// match pa_proplist_sets, whose lengths include the terminator.
func (w *paTagWriter) putProplist(props [][2]string) {
	w.buf = append(w.buf, paTagProplist)
	for _, kv := range props {
		w.putString(kv[0])
		val := make([]byte, 0, len(kv[1])+1)
		val = append(val, kv[1]...)
		val = append(val, 0)
		w.putU32(uint32(len(val)))
		w.putArbitrary(val)
	}
	w.putNullString()
}

// paTagReader parses a tagstruct payload.
type paTagReader struct {
	buf []byte
	pos int
}

func (r *paTagReader) eof() bool { return r.pos >= len(r.buf) }

func (r *paTagReader) take(n int) ([]byte, error) {
	if n < 0 || r.pos+n > len(r.buf) {
		return nil, errPAShortRead
	}
	b := r.buf[r.pos : r.pos+n]
	r.pos += n
	return b, nil
}

func (r *paTagReader) peekTag() (byte, error) {
	if r.pos >= len(r.buf) {
		return 0, errPAShortRead
	}
	return r.buf[r.pos], nil
}

func (r *paTagReader) expect(tag byte) error {
	b, err := r.take(1)
	if err != nil {
		return err
	}
	if b[0] != tag {
		return errPAShortRead
	}
	return nil
}

func (r *paTagReader) getU8() (uint8, error) {
	if err := r.expect(paTagU8); err != nil {
		return 0, err
	}
	b, err := r.take(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (r *paTagReader) getU32() (uint32, error) {
	if err := r.expect(paTagU32); err != nil {
		return 0, err
	}
	b, err := r.take(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}

// getU64 accepts any of the 64 bit tags (u64, s64, usec).
func (r *paTagReader) getU64() (uint64, error) {
	tag, err := r.peekTag()
	if err != nil {
		return 0, err
	}
	if tag != paTagU64 && tag != paTagUsec && tag != paTagS64 {
		return 0, errPAShortRead
	}
	r.pos++
	b, err := r.take(8)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(b), nil
}

// getString returns the next string. A null string yields ("", false).
func (r *paTagReader) getString() (string, bool, error) {
	tag, err := r.peekTag()
	if err != nil {
		return "", false, err
	}
	if tag == paTagStringNull {
		r.pos++
		return "", false, nil
	}
	if tag != paTagString {
		return "", false, errPAShortRead
	}
	r.pos++
	for i := r.pos; i < len(r.buf); i++ {
		if r.buf[i] == 0 {
			s := string(r.buf[r.pos:i])
			r.pos = i + 1
			return s, true, nil
		}
	}
	return "", false, errPAShortRead
}

func (r *paTagReader) getBool() (bool, error) {
	b, err := r.take(1)
	if err != nil {
		return false, err
	}
	switch b[0] {
	case paTagBoolTrue:
		return true, nil
	case paTagBoolFalse:
		return false, nil
	}
	return false, errPAShortRead
}

func (r *paTagReader) getArbitrary() ([]byte, error) {
	if err := r.expect(paTagArbitrary); err != nil {
		return nil, err
	}
	b, err := r.take(4)
	if err != nil {
		return nil, err
	}
	return r.take(int(binary.BigEndian.Uint32(b)))
}

// getSampleSpec returns format, channels and rate.
func (r *paTagReader) getSampleSpec() (format, channels uint8, rate uint32, err error) {
	if err := r.expect(paTagSampleSpec); err != nil {
		return 0, 0, 0, err
	}
	b, err := r.take(6)
	if err != nil {
		return 0, 0, 0, err
	}
	return b[0], b[1], binary.BigEndian.Uint32(b[2:]), nil
}

func (r *paTagReader) getChannelMap() ([]uint8, error) {
	if err := r.expect(paTagChannelMap); err != nil {
		return nil, err
	}
	n, err := r.take(1)
	if err != nil {
		return nil, err
	}
	return r.take(int(n[0]))
}

func (r *paTagReader) getCVolume() ([]uint32, error) {
	if err := r.expect(paTagCVolume); err != nil {
		return nil, err
	}
	n, err := r.take(1)
	if err != nil {
		return nil, err
	}
	out := make([]uint32, n[0])
	for i := range out {
		b, err := r.take(4)
		if err != nil {
			return nil, err
		}
		out[i] = binary.BigEndian.Uint32(b)
	}
	return out, nil
}

// skipProplist consumes a property list without interpreting it.
func (r *paTagReader) skipProplist() error {
	if err := r.expect(paTagProplist); err != nil {
		return err
	}
	for {
		_, ok, err := r.getString()
		if err != nil {
			return err
		}
		if !ok {
			return nil // Terminating null string.
		}
		if _, err := r.getU32(); err != nil {
			return err
		}
		if _, err := r.getArbitrary(); err != nil {
			return err
		}
	}
}

// paPacket is one received packet.
type paPacket struct {
	channel uint32
	data    []byte
}

// isCommand reports whether the packet carries a tagstruct rather than PCM.
func (p *paPacket) isCommand() bool { return p.channel == paChannelCommand }

// paConn is a framed connection to the PulseAudio daemon.
type paConn struct {
	conn *net.UnixConn

	writeMu sync.Mutex
	hdr     [paDescriptorSize]byte

	rdHdr [paDescriptorSize]byte
}

// paSocketPath returns the daemon socket path, honoring the same
// environment variables libpulse uses.
func paSocketPath() string {
	if s := os.Getenv("PULSE_SERVER"); s != "" {
		// Only local sockets are supported: "unix:/path" or a bare path.
		if rest, ok := strings.CutPrefix(s, "unix:"); ok {
			return rest
		}
		if strings.HasPrefix(s, "/") {
			return s
		}
	}
	if p := os.Getenv("PULSE_RUNTIME_PATH"); p != "" {
		return filepath.Join(p, "native")
	}
	if p := os.Getenv("XDG_RUNTIME_DIR"); p != "" {
		return filepath.Join(p, "pulse", "native")
	}
	return filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "pulse", "native")
}

// paReadCookie loads the authentication cookie. A missing cookie is not
// fatal: on a local socket the daemon also accepts SCM_CREDENTIALS, so an
// all-zero cookie is returned and authentication falls back to that.
func paReadCookie() []byte {
	var paths []string
	if p := os.Getenv("PULSE_COOKIE"); p != "" {
		paths = append(paths, p)
	}
	if p := os.Getenv("XDG_CONFIG_HOME"); p != "" {
		paths = append(paths, filepath.Join(p, "pulse", "cookie"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths,
			filepath.Join(home, ".config", "pulse", "cookie"),
			filepath.Join(home, ".pulse-cookie"))
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil || len(b) < paNativeCookieBytes {
			continue
		}
		return b[:paNativeCookieBytes]
	}
	return make([]byte, paNativeCookieBytes)
}

// dialPulse connects to the daemon socket.
func dialPulse() (*paConn, error) {
	addr := &net.UnixAddr{Name: paSocketPath(), Net: "unix"}
	conn, err := net.DialUnix("unix", nil, addr)
	if err != nil {
		return nil, ErrFailedToInitBackend
	}
	return &paConn{conn: conn}, nil
}

func (c *paConn) close() {
	c.conn.Close()
}

// sendCommand writes a tagstruct packet. When withCreds is set the packet
// carries SCM_CREDENTIALS, which is how the daemon authenticates a local
// client whose cookie is missing or stale.
func (c *paConn) sendCommand(payload []byte, withCreds bool) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	binary.BigEndian.PutUint32(c.hdr[0:], uint32(len(payload)))
	binary.BigEndian.PutUint32(c.hdr[4:], paChannelCommand)
	binary.BigEndian.PutUint32(c.hdr[8:], 0)
	binary.BigEndian.PutUint32(c.hdr[12:], 0)
	binary.BigEndian.PutUint32(c.hdr[16:], 0)

	msg := make([]byte, 0, paDescriptorSize+len(payload))
	msg = append(msg, c.hdr[:]...)
	msg = append(msg, payload...)

	var oob []byte
	if withCreds {
		oob = unix.UnixCredentials(&unix.Ucred{
			Pid: int32(os.Getpid()),
			Uid: uint32(os.Getuid()),
			Gid: uint32(os.Getgid()),
		})
	}
	return c.writeMsg(msg, oob)
}

// sendData writes a PCM payload for the given stream channel.
func (c *paConn) sendData(channel uint32, offset int64, seek uint32, pcm []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	binary.BigEndian.PutUint32(c.hdr[0:], uint32(len(pcm)))
	binary.BigEndian.PutUint32(c.hdr[4:], channel)
	binary.BigEndian.PutUint32(c.hdr[8:], uint32(uint64(offset)>>32))
	binary.BigEndian.PutUint32(c.hdr[12:], uint32(uint64(offset)))
	binary.BigEndian.PutUint32(c.hdr[16:], seek)

	msg := make([]byte, 0, paDescriptorSize+len(pcm))
	msg = append(msg, c.hdr[:]...)
	msg = append(msg, pcm...)
	return c.writeMsg(msg, nil)
}

// writeMsg writes msg, and any ancillary data, in full.
func (c *paConn) writeMsg(msg, oob []byte) error {
	if len(oob) > 0 {
		sc, err := c.conn.SyscallConn()
		if err != nil {
			return ErrIOError
		}
		var sendErr error
		var n int
		err = sc.Write(func(fd uintptr) bool {
			n, sendErr = unix.SendmsgN(int(fd), msg, oob, nil, 0)
			return sendErr != unix.EAGAIN
		})
		if err != nil || sendErr != nil {
			return ErrIOError
		}
		if n >= len(msg) {
			return nil
		}
		msg = msg[n:]
	}
	for len(msg) > 0 {
		n, err := c.conn.Write(msg)
		if err != nil {
			return ErrIOError
		}
		msg = msg[n:]
	}
	return nil
}

// recv reads the next packet, blocking until one arrives.
func (c *paConn) recv() (*paPacket, error) {
	if err := readFullUnix(c.conn, c.rdHdr[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(c.rdHdr[0:])
	channel := binary.BigEndian.Uint32(c.rdHdr[4:])
	flags := binary.BigEndian.Uint32(c.rdHdr[16:])

	// SHM descriptors reference a shared block rather than carrying the
	// payload. This client never negotiates shm, so seeing one means the
	// server is misbehaving.
	if flags&paProtoFlagSHM != 0 {
		return nil, ErrBadProtocol
	}
	if length > paMaxPacketSize {
		return nil, ErrBadProtocol
	}

	data := make([]byte, length)
	if length > 0 {
		if err := readFullUnix(c.conn, data); err != nil {
			return nil, err
		}
	}
	return &paPacket{channel: channel, data: data}, nil
}

// readFullUnix fills buf completely.
func readFullUnix(conn *net.UnixConn, buf []byte) error {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		if n > 0 {
			total += n
		}
		if err != nil {
			return ErrIOError
		}
	}
	return nil
}
