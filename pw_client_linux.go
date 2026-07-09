package gominiaudio

import (
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// PipeWire protocol constants.

// Core methods (PW_CORE_METHOD_*).
const (
	pwCoreMethodHello        = 1
	pwCoreMethodSync         = 2
	pwCoreMethodPong         = 3
	pwCoreMethodError        = 4
	pwCoreMethodGetRegistry  = 5
	pwCoreMethodCreateObject = 6
	pwCoreMethodDestroy      = 7
)

// Core events (PW_CORE_EVENT_*).
const (
	pwCoreEventInfo     = 0
	pwCoreEventDone     = 1
	pwCoreEventPing     = 2
	pwCoreEventError    = 3
	pwCoreEventRemoveID = 4
	pwCoreEventBoundID  = 5
	pwCoreEventAddMem   = 6
	pwCoreEventRemoveMem = 7
)

// Client methods.
const (
	pwClientMethodUpdateProperties = 2
)

// Registry methods/events.
const (
	pwRegistryMethodBind   = 1
	pwRegistryEventGlobal  = 0
	pwRegistryEventGlobalRemove = 1
)

// ClientNode methods (PW_CLIENT_NODE_METHOD_*).
const (
	pwClientNodeMethodGetNode     = 1
	pwClientNodeMethodUpdate      = 2
	pwClientNodeMethodPortUpdate  = 3
	pwClientNodeMethodSetActive   = 4
	pwClientNodeMethodEvent       = 5
	pwClientNodeMethodPortBuffers = 6
)

// ClientNode events (PW_CLIENT_NODE_EVENT_*).
const (
	pwClientNodeEventTransport      = 0
	pwClientNodeEventSetParam       = 1
	pwClientNodeEventSetIO          = 2
	pwClientNodeEventEvent          = 3
	pwClientNodeEventCommand        = 4
	pwClientNodeEventAddPort        = 5
	pwClientNodeEventRemovePort     = 6
	pwClientNodeEventPortSetParam   = 7
	pwClientNodeEventUseBuffers     = 8
	pwClientNodeEventPortSetIO      = 9
	pwClientNodeEventSetActivation  = 10
	pwClientNodeEventPortSetMixInfo = 11
)

// Node/port update masks.
const (
	pwClientNodeUpdateParams = 1 << 0
	pwClientNodeUpdateInfo   = 1 << 1

	spaNodeChangeMaskFlags  = 1 << 0
	spaNodeChangeMaskProps  = 1 << 1
	spaNodeChangeMaskParams = 1 << 2

	spaPortChangeMaskFlags  = 1 << 0
	spaPortChangeMaskRate   = 1 << 1
	spaPortChangeMaskProps  = 1 << 2
	spaPortChangeMaskParams = 1 << 3
)

// SPA param ids (spa/param/param.h).
const (
	spaParamPropInfo   = 1
	spaParamProps      = 2
	spaParamEnumFormat = 3
	spaParamFormat     = 4
	spaParamBuffers    = 5
	spaParamMeta       = 6
	spaParamIO         = 7
)

// SPA param info flags.
const (
	spaParamInfoSerial = 1 << 0
	spaParamInfoRead   = 1 << 1
	spaParamInfoWrite  = 1 << 2
	spaParamInfoReadWrite = spaParamInfoRead | spaParamInfoWrite
)

// SPA object types.
const (
	spaTypeObjectPropInfo     = 0x40001
	spaTypeObjectProps        = 0x40002
	spaTypeObjectFormat       = 0x40003
	spaTypeObjectParamBuffers = 0x40004
	spaTypeObjectParamMeta    = 0x40005
	spaTypeObjectParamIO      = 0x40006

	spaTypeCommandNode = 0x30002
)

// SPA format keys.
const (
	spaFormatMediaType     = 1
	spaFormatMediaSubtype  = 2
	spaFormatAudioFormat   = 0x10001
	spaFormatAudioFlags    = 0x10002
	spaFormatAudioRate     = 0x10003
	spaFormatAudioChannels = 0x10004
	spaFormatAudioPosition = 0x10005
)

// SPA media types/subtypes.
const (
	spaMediaTypeAudio    = 1
	spaMediaSubtypeRaw   = 1
)

// SPA audio formats (spa/param/audio/raw.h). Little endian variants.
const (
	spaAudioFormatS16LE = 0x103
	spaAudioFormatS32LE = 0x10B
	spaAudioFormatS24LE = 0x10F
	spaAudioFormatU8    = 0x102
	spaAudioFormatF32LE = 0x11B
)

// SPA audio channel positions (subset).
const (
	spaAudioChannelMono = 2
	spaAudioChannelFL   = 3
	spaAudioChannelFR   = 4
	spaAudioChannelFC   = 5
	spaAudioChannelLFE  = 6
	spaAudioChannelSL   = 7
	spaAudioChannelSR   = 8
	spaAudioChannelRL   = 12
	spaAudioChannelRR   = 13
)

// SPA buffers param keys.
const (
	spaParamBuffersBuffers  = 1
	spaParamBuffersBlocks   = 2
	spaParamBuffersSize     = 3
	spaParamBuffersStride   = 4
	spaParamBuffersAlign    = 5
	spaParamBuffersDataType = 6
)

// SPA meta param keys.
const (
	spaParamMetaType = 1
	spaParamMetaSize = 2
)

// SPA IO param keys.
const (
	spaParamIOID   = 1
	spaParamIOSize = 2
)

// SPA IO area ids (spa/node/io.h).
const (
	spaIOBuffers  = 1
	spaIOClock    = 3
	spaIOPosition = 7
	spaIORateMatch = 8
)

// spa_io_buffers status codes.
const (
	spaStatusOK       = 0
	spaStatusNeedData = 1
	spaStatusHaveData = 2
	spaStatusStopped  = 4
	spaStatusDrained  = 8
)

// SPA data types.
const (
	spaDataMemPtr = 1
	spaDataMemFd  = 2
	spaDataMemID  = 4
)

// SPA meta types.
const (
	spaMetaHeader = 1
)

// Node commands (spa/node/command.h, object id of SPA_TYPE_COMMAND_Node).
const (
	spaNodeCommandSuspend = 0
	spaNodeCommandPause   = 1
	spaNodeCommandStart   = 2
)

// Directions.
const (
	spaDirectionInput  = 0
	spaDirectionOutput = 1
)

// Activation record offsets (struct pw_node_activation, pipewire
// src/pipewire/private.h). Only the fields the client touches.
const (
	activationOffStatus        = 0
	activationOffState0Status  = 8
	activationOffState0Required = 12
	activationOffState0Pending = 16
	activationOffSignalTime    = 32
	activationOffAwakeTime     = 40
	activationOffFinishTime    = 48
)

// pw_node_activation status values.
const (
	pwNodeActivationNotTriggered = 0
	pwNodeActivationTriggered    = 1
	pwNodeActivationAwake        = 2
	pwNodeActivationFinished     = 3
)

// spa_io_position/clock offsets (spa/node/io.h struct spa_io_clock at the
// start of spa_io_position): duration and rate for quantum discovery.
const (
	clockOffRateNum  = 80
	clockOffRateDenom = 84
	clockOffPosition = 88
	clockOffDuration = 96
)

// pwDict is a string property dictionary.
type pwDict [][2]string

func (b *podBuilder) addDict(d pwDict) {
	b.addInt(int32(len(d)))
	for _, kv := range d {
		b.addString(kv[0])
		b.addString(kv[1])
	}
}

/**************************************************************************
Core client
**************************************************************************/

// pwGlobal is one registry global object.
type pwGlobal struct {
	ID          uint32
	Type        string
	Version     uint32
	Props       map[string]string
}

// pwMem is one entry in the shared memory table (from AddMem events).
type pwMem struct {
	fd    int
	typ   uint32
	flags uint32
}

// pwCore manages the connection, proxy ids and core-level events.
type pwCore struct {
	conn *pwConn

	mu        sync.Mutex
	nextProxy uint32
	mems      map[uint32]*pwMem
	globals   map[uint32]*pwGlobal
	registryID uint32

	doneCh   chan uint32 // Delivers core Done sequence numbers.
	errCh    chan error
	handlers map[uint32]func(opcode uint8, msg *pwMessage) // Per-proxy event handlers.

	loopDone chan struct{}
	closed   atomic.Bool
}

// pwConnect dials the daemon and performs the handshake.
func pwConnect(appName string) (*pwCore, error) {
	conn, err := dialPipeWire()
	if err != nil {
		return nil, err
	}
	c := &pwCore{
		conn:      conn,
		nextProxy: 2, // 0 = core, 1 = client.
		mems:      make(map[uint32]*pwMem),
		globals:   make(map[uint32]*pwGlobal),
		doneCh:    make(chan uint32, 16),
		errCh:     make(chan error, 4),
		handlers:  make(map[uint32]func(uint8, *pwMessage)),
		loopDone:  make(chan struct{}),
	}
	go c.eventLoop()

	// Hello + client properties.
	var b podBuilder
	f := b.pushStruct()
	b.addInt(3) // PW_VERSION_CORE
	b.pop(f)
	if err := c.conn.send(pwCoreID, pwCoreMethodHello, b.bytes(), nil); err != nil {
		c.close()
		return nil, err
	}

	b = podBuilder{}
	f = b.pushStruct()
	fd := b.pushStruct()
	b.addDict(pwDict{
		{"application.name", appName},
		{"application.process.binary", appName},
	})
	b.pop(fd)
	b.pop(f)
	if err := c.conn.send(pwClientID, pwClientMethodUpdateProperties, b.bytes(), nil); err != nil {
		c.close()
		return nil, err
	}

	if err := c.roundtrip(); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

func (c *pwCore) close() {
	if c.closed.Swap(true) {
		return
	}
	c.conn.close()
	<-c.loopDone
	c.mu.Lock()
	for _, m := range c.mems {
		unix.Close(m.fd)
	}
	c.mems = nil
	c.mu.Unlock()
}

func (c *pwCore) allocProxyID() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextProxy
	c.nextProxy++
	return id
}

func (c *pwCore) setHandler(proxyID uint32, fn func(uint8, *pwMessage)) {
	c.mu.Lock()
	c.handlers[proxyID] = fn
	c.mu.Unlock()
}

// eventLoop dispatches incoming messages.
func (c *pwCore) eventLoop() {
	defer close(c.loopDone)
	for {
		msg, err := c.conn.recv()
		if err != nil {
			select {
			case c.errCh <- err:
			default:
			}
			return
		}
		c.dispatch(msg)
	}
}

func (c *pwCore) dispatch(msg *pwMessage) {
	switch msg.ID {
	case pwCoreID:
		c.handleCoreEvent(msg)
		return
	}
	c.mu.Lock()
	h := c.handlers[msg.ID]
	c.mu.Unlock()
	if h != nil {
		h(msg.Opcode, msg)
		return
	}
	// Unhandled proxy: close any fds to avoid leaks.
	for _, fd := range msg.Fds {
		unix.Close(fd)
	}
}

func (c *pwCore) handleCoreEvent(msg *pwMessage) {
	switch msg.Opcode {
	case pwCoreEventDone:
		v, _, ok := parsePod(msg.Data)
		if !ok {
			return
		}
		p := v.structParser()
		p.nextInt() // id
		seq, _ := p.nextInt()
		select {
		case c.doneCh <- uint32(seq):
		default:
		}
	case pwCoreEventPing:
		v, _, ok := parsePod(msg.Data)
		if !ok {
			return
		}
		p := v.structParser()
		id, _ := p.nextInt()
		seq, _ := p.nextInt()
		var b podBuilder
		f := b.pushStruct()
		b.addInt(id)
		b.addInt(seq)
		b.pop(f)
		c.conn.send(pwCoreID, pwCoreMethodPong, b.bytes(), nil)
	case pwCoreEventAddMem:
		v, _, ok := parsePod(msg.Data)
		if !ok {
			return
		}
		p := v.structParser()
		id, _ := p.nextInt()
		typ, _ := p.nextInt()
		fdIdx, _ := p.nextFd()
		flags, _ := p.nextInt()
		if int(fdIdx) < len(msg.Fds) {
			c.mu.Lock()
			c.mems[uint32(id)] = &pwMem{fd: msg.Fds[fdIdx], typ: uint32(typ), flags: uint32(flags)}
			c.mu.Unlock()
		}
	case pwCoreEventRemoveMem:
		v, _, ok := parsePod(msg.Data)
		if !ok {
			return
		}
		p := v.structParser()
		id, _ := p.nextInt()
		c.mu.Lock()
		if m := c.mems[uint32(id)]; m != nil {
			unix.Close(m.fd)
			delete(c.mems, uint32(id))
		}
		c.mu.Unlock()
	case pwCoreEventError:
		select {
		case c.errCh <- ErrorGeneric:
		default:
		}
	}
}

func (c *pwCore) mem(id uint32) *pwMem {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mems[id]
}

// roundtrip sends a core Sync and waits for the matching Done.
func (c *pwCore) roundtrip() error {
	var b podBuilder
	f := b.pushStruct()
	b.addInt(pwCoreID)
	seq := int32(0x5A5A)
	b.addInt(seq)
	b.pop(f)
	if err := c.conn.send(pwCoreID, pwCoreMethodSync, b.bytes(), nil); err != nil {
		return err
	}
	timeout := time.After(5 * time.Second)
	for {
		select {
		case s := <-c.doneCh:
			if s == uint32(seq) {
				return nil
			}
		case err := <-c.errCh:
			return err
		case <-timeout:
			return ErrTimeout
		}
	}
}

// getRegistry binds the registry and collects globals until a sync
// completes.
func (c *pwCore) getRegistry() error {
	regID := c.allocProxyID()
	c.mu.Lock()
	c.registryID = regID
	c.mu.Unlock()

	c.setHandler(regID, func(opcode uint8, msg *pwMessage) {
		if opcode != pwRegistryEventGlobal {
			return
		}
		v, _, ok := parsePod(msg.Data)
		if !ok {
			return
		}
		p := v.structParser()
		id, _ := p.nextInt()
		p.nextInt() // permissions
		typ, _ := p.nextString()
		version, _ := p.nextInt()
		props := map[string]string{}
		if propsPod, ok := p.next(); ok && propsPod.Type == spaTypeStruct {
			pp := propsPod.structParser()
			n, _ := pp.nextInt()
			for i := int32(0); i < n; i++ {
				k, ok1 := pp.nextString()
				val, ok2 := pp.nextString()
				if !ok1 || !ok2 {
					break
				}
				props[k] = val
			}
		}
		c.mu.Lock()
		c.globals[uint32(id)] = &pwGlobal{ID: uint32(id), Type: typ, Version: uint32(version), Props: props}
		c.mu.Unlock()
	})

	var b podBuilder
	f := b.pushStruct()
	b.addInt(3) // PW_VERSION_REGISTRY
	b.addInt(int32(regID))
	b.pop(f)
	if err := c.conn.send(pwCoreID, pwCoreMethodGetRegistry, b.bytes(), nil); err != nil {
		return err
	}
	return c.roundtrip()
}

// createClientNode creates a client-node object and returns its proxy id.
func (c *pwCore) createClientNode(props pwDict) (uint32, error) {
	id := c.allocProxyID()
	var b podBuilder
	f := b.pushStruct()
	b.addString("client-node")
	b.addString("PipeWire:Interface:ClientNode")
	b.addInt(6) // PW_VERSION_CLIENT_NODE
	fd := b.pushStruct()
	b.addDict(props)
	b.pop(fd)
	b.addInt(int32(id))
	b.pop(f)
	if err := c.conn.send(pwCoreID, pwCoreMethodCreateObject, b.bytes(), nil); err != nil {
		return 0, err
	}
	return id, nil
}

/**************************************************************************
Memory mapping helpers
**************************************************************************/

// pwMapping is a mapped shared memory region.
type pwMapping struct {
	data []byte
}

func (c *pwCore) mapMemory(memID uint32, offset, size int32, writable bool) (*pwMapping, error) {
	m := c.mem(memID)
	if m == nil || size <= 0 {
		return nil, ErrInvalidArgs
	}
	prot := unix.PROT_READ
	if writable {
		prot |= unix.PROT_WRITE
	}
	// Mmap offsets must be page aligned.
	pageSize := int32(unix.Getpagesize())
	alignedOff := offset &^ (pageSize - 1)
	diff := offset - alignedOff
	data, err := unix.Mmap(m.fd, int64(alignedOff), int(size+diff), prot, unix.MAP_SHARED)
	if err != nil {
		return nil, ErrBadAddress
	}
	return &pwMapping{data: data[diff : diff+size]}, nil
}

func (mp *pwMapping) unmap() {
	if mp != nil && mp.data != nil {
		// Recover the full mapping slice for munmap: not strictly exact
		// (offset trimmed), but the kernel unmaps by page anyway.
		unix.Munmap(mp.data[:cap(mp.data)])
		mp.data = nil
	}
}

// Atomic accessors into mapped memory.

func atomicLoadI32(b []byte, off int) int32 {
	return atomic.LoadInt32((*int32)(unsafe.Pointer(&b[off])))
}

func atomicStoreI32(b []byte, off int, v int32) {
	atomic.StoreInt32((*int32)(unsafe.Pointer(&b[off])), v)
}

func atomicAddI32(b []byte, off int, delta int32) int32 {
	return atomic.AddInt32((*int32)(unsafe.Pointer(&b[off])), delta)
}

func atomicStoreU32(b []byte, off int, v uint32) {
	atomic.StoreUint32((*uint32)(unsafe.Pointer(&b[off])), v)
}

func atomicLoadU32(b []byte, off int) uint32 {
	return atomic.LoadUint32((*uint32)(unsafe.Pointer(&b[off])))
}

func atomicStoreU64(b []byte, off int, v uint64) {
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&b[off])), v)
}

