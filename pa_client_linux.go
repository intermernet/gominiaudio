//go:build linux && !android

package gominiaudio

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// PulseAudio client: connection setup, request/reply correlation and the
// enumeration commands. Stream handling lives in backend_pulseaudio_linux.go.

// paPendingReply is a request awaiting its reply.
type paPendingReply struct {
	ch chan paReply
}

// paReply is a completed request.
type paReply struct {
	data []byte // Tagstruct payload, positioned after command and tag.
	err  error
}

// paStreamHandler receives the asynchronous events for one stream.
type paStreamHandler interface {
	// onRequest is called when the server asks for more playback data.
	onRequest(nbytes uint32)
	// onData is called with captured PCM for a record stream.
	onData(pcm []byte)
	// onKilled is called when the server destroys the stream.
	onKilled()
}

// paClient is a connection to the PulseAudio daemon.
type paClient struct {
	conn *paConn

	mu       sync.Mutex
	nextTag  uint32
	pending  map[uint32]*paPendingReply
	streams  map[uint32]paStreamHandler
	closed   bool
	closeErr error

	version uint32 // Negotiated protocol version.

	doneCh chan struct{}
	once   sync.Once
}

// paConnect opens a connection, authenticates and registers the client name.
func paConnect(appName string) (*paClient, error) {
	conn, err := dialPulse()
	if err != nil {
		return nil, err
	}
	c := &paClient{
		conn:    conn,
		nextTag: 1,
		pending: make(map[uint32]*paPendingReply),
		streams: make(map[uint32]paStreamHandler),
		doneCh:  make(chan struct{}),
	}
	go c.readLoop()

	if err := c.auth(); err != nil {
		c.close()
		return nil, err
	}
	if err := c.setClientName(appName); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

// auth performs the AUTH handshake and records the negotiated version.
func (c *paClient) auth() error {
	var w paTagWriter
	w.putU32(paCommandAuth)
	tag := c.allocTag()
	w.putU32(tag)
	// The SHM flag is deliberately not set: this client always takes PCM
	// inline through the socket, which avoids mapping the server's memory
	// pool and works identically against PipeWire's pulse-server.
	w.putU32(paProtocolVersion)
	w.putArbitrary(paReadCookie())

	reply, err := c.requestWithTag(tag, w.buf, true)
	if err != nil {
		return err
	}
	r := &paTagReader{buf: reply}
	v, err := r.getU32()
	if err != nil {
		return ErrBadProtocol
	}
	c.mu.Lock()
	c.version = v & paProtoVersionMask
	c.mu.Unlock()
	if c.version < 8 {
		return ErrBadProtocol
	}
	return nil
}

// setClientName registers this client's proplist with the server.
func (c *paClient) setClientName(appName string) error {
	if appName == "" {
		appName = "gominiaudio"
	}
	var w paTagWriter
	w.putU32(paCommandSetClientName)
	tag := c.allocTag()
	w.putU32(tag)
	w.putProplist([][2]string{
		{"application.name", appName},
		{"application.process.id", strconv.Itoa(os.Getpid())},
		{"application.process.binary", filepath.Base(os.Args[0])},
		{"media.role", "music"},
	})
	_, err := c.requestWithTag(tag, w.buf, false)
	return err
}

// allocTag reserves a request tag.
func (c *paClient) allocTag() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.nextTag
	c.nextTag++
	return t
}

// requestWithTag sends a prepared payload and waits for its reply. The tag
// must already be encoded in payload.
func (c *paClient) requestWithTag(tag uint32, payload []byte, withCreds bool) ([]byte, error) {
	p := &paPendingReply{ch: make(chan paReply, 1)}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrDeviceNotInitialized
	}
	c.pending[tag] = p
	c.mu.Unlock()

	if err := c.conn.sendCommand(payload, withCreds); err != nil {
		c.mu.Lock()
		delete(c.pending, tag)
		c.mu.Unlock()
		return nil, err
	}

	select {
	case rep := <-p.ch:
		return rep.data, rep.err
	case <-c.doneCh:
		return nil, ErrIOError
	}
}

// request encodes command+tag, sends it and waits for the reply.
func (c *paClient) request(command uint32, build func(w *paTagWriter)) ([]byte, error) {
	var w paTagWriter
	w.putU32(command)
	tag := c.allocTag()
	w.putU32(tag)
	if build != nil {
		build(&w)
	}
	return c.requestWithTag(tag, w.buf, false)
}

// registerStream associates a stream channel with its handler.
func (c *paClient) registerStream(channel uint32, h paStreamHandler) {
	c.mu.Lock()
	c.streams[channel] = h
	c.mu.Unlock()
}

func (c *paClient) unregisterStream(channel uint32) {
	c.mu.Lock()
	delete(c.streams, channel)
	c.mu.Unlock()
}

// readLoop dispatches incoming packets until the connection drops.
func (c *paClient) readLoop() {
	for {
		pkt, err := c.conn.recv()
		if err != nil {
			c.fail(err)
			return
		}
		if !pkt.isCommand() {
			c.dispatchData(pkt)
			continue
		}
		if err := c.dispatchCommand(pkt.data); err != nil {
			c.fail(err)
			return
		}
	}
}

// dispatchData routes a PCM packet to its record stream.
func (c *paClient) dispatchData(pkt *paPacket) {
	c.mu.Lock()
	h := c.streams[pkt.channel]
	c.mu.Unlock()
	if h != nil {
		h.onData(pkt.data)
	}
}

// dispatchCommand routes a tagstruct packet: replies to their waiter,
// events to their stream.
func (c *paClient) dispatchCommand(payload []byte) error {
	r := &paTagReader{buf: payload}
	command, err := r.getU32()
	if err != nil {
		return ErrBadProtocol
	}
	tag, err := r.getU32()
	if err != nil {
		return ErrBadProtocol
	}

	switch command {
	case paCommandReply:
		c.completeReply(tag, paReply{data: payload[r.pos:]})
		return nil

	case paCommandError:
		code, err := r.getU32()
		if err != nil {
			return ErrBadProtocol
		}
		c.completeReply(tag, paReply{err: paErrorToResult(code)})
		return nil

	case paCommandRequest:
		channel, err := r.getU32()
		if err != nil {
			return ErrBadProtocol
		}
		nbytes, err := r.getU32()
		if err != nil {
			return ErrBadProtocol
		}
		c.mu.Lock()
		h := c.streams[channel]
		c.mu.Unlock()
		if h != nil {
			h.onRequest(nbytes)
		}
		return nil

	case paCommandPlaybackStreamKilled, paCommandRecordStreamKilled:
		channel, err := r.getU32()
		if err != nil {
			return ErrBadProtocol
		}
		c.mu.Lock()
		h := c.streams[channel]
		c.mu.Unlock()
		if h != nil {
			h.onKilled()
		}
		return nil
	}

	// Everything else (overflow, underflow, moved, suspended, subscribe
	// events) is informational for this backend.
	return nil
}

// completeReply hands a reply to its waiter.
func (c *paClient) completeReply(tag uint32, rep paReply) {
	c.mu.Lock()
	p := c.pending[tag]
	delete(c.pending, tag)
	c.mu.Unlock()
	if p != nil {
		p.ch <- rep
	}
}

// fail tears down the connection and wakes every waiter.
func (c *paClient) fail(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.closeErr = err
	pending := c.pending
	c.pending = make(map[uint32]*paPendingReply)
	streams := c.streams
	c.streams = make(map[uint32]paStreamHandler)
	c.mu.Unlock()

	for _, p := range pending {
		p.ch <- paReply{err: err}
	}
	for _, h := range streams {
		h.onKilled()
	}
	c.once.Do(func() { close(c.doneCh) })
}

// close shuts the connection down.
func (c *paClient) close() {
	c.mu.Lock()
	alreadyClosed := c.closed
	c.closed = true
	c.mu.Unlock()
	if !alreadyClosed {
		c.once.Do(func() { close(c.doneCh) })
	}
	c.conn.close()
}

// paServerInfo is the subset of GET_SERVER_INFO this backend uses.
type paServerInfo struct {
	DefaultSink   string
	DefaultSource string
	SampleFormat  uint8
	Channels      uint8
	SampleRate    uint32
}

// serverInfo queries the default devices and the server's sample spec.
func (c *paClient) serverInfo() (paServerInfo, error) {
	payload, err := c.request(paCommandGetServerInfo, nil)
	if err != nil {
		return paServerInfo{}, err
	}
	r := &paTagReader{buf: payload}
	var info paServerInfo
	if _, _, err := r.getString(); err != nil { // Package name.
		return info, ErrBadProtocol
	}
	if _, _, err := r.getString(); err != nil { // Package version.
		return info, ErrBadProtocol
	}
	if _, _, err := r.getString(); err != nil { // User name.
		return info, ErrBadProtocol
	}
	if _, _, err := r.getString(); err != nil { // Host name.
		return info, ErrBadProtocol
	}
	format, channels, rate, err := r.getSampleSpec()
	if err != nil {
		return info, ErrBadProtocol
	}
	info.SampleFormat, info.Channels, info.SampleRate = format, channels, rate
	if s, _, err := r.getString(); err == nil {
		info.DefaultSink = s
	} else {
		return info, ErrBadProtocol
	}
	if s, _, err := r.getString(); err == nil {
		info.DefaultSource = s
	} else {
		return info, ErrBadProtocol
	}
	return info, nil
}

// paDeviceEntry is one sink or source as reported by the server.
type paDeviceEntry struct {
	Index       uint32
	Name        string
	Description string
	Format      uint8
	Channels    uint8
	SampleRate  uint32
	ChannelMap  []uint8
}

// sinks lists the server's sinks.
func (c *paClient) sinks() ([]paDeviceEntry, error) {
	return c.deviceList(paCommandGetSinkInfoList)
}

// sources lists the server's sources, monitors included.
func (c *paClient) sources() ([]paDeviceEntry, error) {
	return c.deviceList(paCommandGetSourceInfoList)
}

// deviceList parses a sink or source info list. Both have identical layouts
// at protocol 13: the common header, then a proplist and the requested
// latency.
func (c *paClient) deviceList(command uint32) ([]paDeviceEntry, error) {
	payload, err := c.request(command, nil)
	if err != nil {
		return nil, err
	}
	r := &paTagReader{buf: payload}
	var out []paDeviceEntry
	for !r.eof() {
		var e paDeviceEntry
		if e.Index, err = r.getU32(); err != nil {
			return nil, ErrBadProtocol
		}
		name, _, err := r.getString()
		if err != nil {
			return nil, ErrBadProtocol
		}
		e.Name = name
		desc, _, err := r.getString()
		if err != nil {
			return nil, ErrBadProtocol
		}
		e.Description = desc
		if e.Format, e.Channels, e.SampleRate, err = r.getSampleSpec(); err != nil {
			return nil, ErrBadProtocol
		}
		if e.ChannelMap, err = r.getChannelMap(); err != nil {
			return nil, ErrBadProtocol
		}
		if _, err := r.getU32(); err != nil { // Owner module.
			return nil, ErrBadProtocol
		}
		if _, err := r.getCVolume(); err != nil {
			return nil, ErrBadProtocol
		}
		if _, err := r.getBool(); err != nil { // Muted.
			return nil, ErrBadProtocol
		}
		if _, err := r.getU32(); err != nil { // Monitor index.
			return nil, ErrBadProtocol
		}
		if _, _, err := r.getString(); err != nil { // Monitor name.
			return nil, ErrBadProtocol
		}
		if _, err := r.getU64(); err != nil { // Latency.
			return nil, ErrBadProtocol
		}
		if _, _, err := r.getString(); err != nil { // Driver.
			return nil, ErrBadProtocol
		}
		if _, err := r.getU32(); err != nil { // Flags.
			return nil, ErrBadProtocol
		}
		if c.version >= 13 {
			if err := r.skipProplist(); err != nil {
				return nil, ErrBadProtocol
			}
			if _, err := r.getU64(); err != nil { // Requested latency.
				return nil, ErrBadProtocol
			}
		}
		out = append(out, e)
	}
	return out, nil
}
