//go:build linux && !android

package gominiaudio

import (
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

// PipeWire native protocol connection. Messages are framed with a 16 byte
// header followed by an SPA POD struct payload; file descriptors travel as
// SCM_RIGHTS ancillary data.
//
// Header layout (native endian):
//   u32 id       - proxy id
//   u32 opcode:8 | size:24
//   u32 seq
//   u32 n_fds

// Well-known proxy IDs.
const (
	pwCoreID   = 0
	pwClientID = 1
)

// pwMessage is one received protocol message.
type pwMessage struct {
	ID     uint32
	Opcode uint8
	Seq    uint32
	Data   []byte
	Fds    []int
}

// pwConn is a connection to the PipeWire daemon.
type pwConn struct {
	conn *net.UnixConn

	writeMu sync.Mutex
	seq     uint32

	// Receive buffering.
	recvBuf []byte
	recvFds []int
}

// pipewireSocketPath returns the daemon socket path.
func pipewireSocketPath() string {
	if p := os.Getenv("PIPEWIRE_RUNTIME_DIR"); p != "" {
		return filepath.Join(p, pipewireSocketName())
	}
	if p := os.Getenv("XDG_RUNTIME_DIR"); p != "" {
		return filepath.Join(p, pipewireSocketName())
	}
	return filepath.Join("/run/user", "0", pipewireSocketName())
}

func pipewireSocketName() string {
	if n := os.Getenv("PIPEWIRE_CORE"); n != "" {
		return n
	}
	return "pipewire-0"
}

// dialPipeWire connects to the daemon.
func dialPipeWire() (*pwConn, error) {
	addr := &net.UnixAddr{Name: pipewireSocketPath(), Net: "unix"}
	conn, err := net.DialUnix("unix", nil, addr)
	if err != nil {
		return nil, ErrFailedToInitBackend
	}
	return &pwConn{conn: conn}, nil
}

func (c *pwConn) close() {
	c.conn.Close()
	for _, fd := range c.recvFds {
		unix.Close(fd)
	}
	c.recvFds = nil
}

// send writes one message. fds are duplicated into the ancillary data.
func (c *pwConn) send(id uint32, opcode uint8, payload []byte, fds []int) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	c.seq++
	var hdr [16]byte
	binary.LittleEndian.PutUint32(hdr[0:], id)
	binary.LittleEndian.PutUint32(hdr[4:], uint32(opcode)<<24|uint32(len(payload))&0xFFFFFF)
	binary.LittleEndian.PutUint32(hdr[8:], c.seq)
	binary.LittleEndian.PutUint32(hdr[12:], uint32(len(fds)))

	msg := make([]byte, 0, 16+len(payload))
	msg = append(msg, hdr[:]...)
	msg = append(msg, payload...)

	var oob []byte
	if len(fds) > 0 {
		oob = unix.UnixRights(fds...)
	}

	sc, err := c.conn.SyscallConn()
	if err != nil {
		return ErrIOError
	}
	var sendErr error
	err = sc.Write(func(fd uintptr) bool {
		_, sendErr = unix.SendmsgN(int(fd), msg, oob, nil, 0)
		return sendErr != unix.EAGAIN
	})
	if err != nil {
		return ErrIOError
	}
	if sendErr != nil {
		return ErrIOError
	}
	return nil
}

// recv reads the next complete message, blocking until one arrives.
func (c *pwConn) recv() (*pwMessage, error) {
	for {
		// Try to parse a message from the buffer.
		if len(c.recvBuf) >= 16 {
			size := int(binary.LittleEndian.Uint32(c.recvBuf[4:]) & 0xFFFFFF)
			nfds := int(binary.LittleEndian.Uint32(c.recvBuf[12:]))
			if len(c.recvBuf) >= 16+size && len(c.recvFds) >= nfds {
				m := &pwMessage{
					ID:     binary.LittleEndian.Uint32(c.recvBuf[0:]),
					Opcode: uint8(binary.LittleEndian.Uint32(c.recvBuf[4:]) >> 24),
					Seq:    binary.LittleEndian.Uint32(c.recvBuf[8:]),
				}
				m.Data = make([]byte, size)
				copy(m.Data, c.recvBuf[16:16+size])
				c.recvBuf = c.recvBuf[16+size:]
				if nfds > 0 {
					m.Fds = c.recvFds[:nfds]
					c.recvFds = c.recvFds[nfds:]
				}
				return m, nil
			}
		}

		// Need more data.
		buf := make([]byte, 65536)
		oob := make([]byte, 1024)
		sc, err := c.conn.SyscallConn()
		if err != nil {
			return nil, ErrIOError
		}
		var n, oobn int
		var recvErr error
		err = sc.Read(func(fd uintptr) bool {
			n, oobn, _, _, recvErr = unix.Recvmsg(int(fd), buf, oob, unix.MSG_CMSG_CLOEXEC)
			return recvErr != unix.EAGAIN
		})
		if err != nil || recvErr != nil {
			return nil, ErrIOError
		}
		if n == 0 {
			return nil, ErrConnectionReset
		}
		c.recvBuf = append(c.recvBuf, buf[:n]...)
		if oobn > 0 {
			msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
			if err == nil {
				for _, m := range msgs {
					if fds, err := unix.ParseUnixRights(&m); err == nil {
						c.recvFds = append(c.recvFds, fds...)
					}
				}
			}
		}
	}
}
