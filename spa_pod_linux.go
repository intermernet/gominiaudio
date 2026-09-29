//go:build linux && !android

package gominiaudio

import (
	"encoding/binary"
	"math"
)

// SPA POD (Plain Old Data) serialization for the PipeWire native protocol.
// PODs are 8-byte aligned TLV values: u32 size, u32 type, payload padded to
// a multiple of 8 bytes. Everything is native endian (little endian on all
// supported platforms).

// SPA type codes (spa/utils/type.h).
const (
	spaTypeNone      = 1
	spaTypeBool      = 2
	spaTypeID        = 3
	spaTypeInt       = 4
	spaTypeLong      = 5
	spaTypeFloat     = 6
	spaTypeDouble    = 7
	spaTypeString    = 8
	spaTypeBytes     = 9
	spaTypeRectangle = 10
	spaTypeFraction  = 11
	spaTypeBitmap    = 12
	spaTypeArray     = 13
	spaTypeStruct    = 14
	spaTypeObject    = 15
	spaTypeSequence  = 16
	spaTypePointer   = 17
	spaTypeFd        = 18
	spaTypeChoice    = 19
	spaTypePod       = 20
)

// SPA choice types.
const (
	spaChoiceNone  = 0
	spaChoiceRange = 1
	spaChoiceStep  = 2
	spaChoiceEnum  = 3
	spaChoiceFlags = 4
)

func align8(n int) int { return (n + 7) &^ 7 }

// podBuilder builds SPA PODs.
type podBuilder struct {
	buf []byte
}

func (b *podBuilder) bytes() []byte { return b.buf }

func (b *podBuilder) writeHeader(size, typ uint32) {
	var hdr [8]byte
	binary.LittleEndian.PutUint32(hdr[0:], size)
	binary.LittleEndian.PutUint32(hdr[4:], typ)
	b.buf = append(b.buf, hdr[:]...)
}

func (b *podBuilder) pad(payloadSize int) {
	for len(b.buf)%8 != 0 {
		b.buf = append(b.buf, 0)
	}
	_ = payloadSize
}

func (b *podBuilder) addNone() {
	b.writeHeader(0, spaTypeNone)
}

func (b *podBuilder) addBool(v bool) {
	b.writeHeader(4, spaTypeBool)
	var p [8]byte
	if v {
		binary.LittleEndian.PutUint32(p[0:], 1)
	}
	b.buf = append(b.buf, p[:]...)
}

func (b *podBuilder) addID(v uint32) {
	b.writeHeader(4, spaTypeID)
	var p [8]byte
	binary.LittleEndian.PutUint32(p[0:], v)
	b.buf = append(b.buf, p[:]...)
}

func (b *podBuilder) addInt(v int32) {
	b.writeHeader(4, spaTypeInt)
	var p [8]byte
	binary.LittleEndian.PutUint32(p[0:], uint32(v))
	b.buf = append(b.buf, p[:]...)
}

func (b *podBuilder) addLong(v int64) {
	b.writeHeader(8, spaTypeLong)
	var p [8]byte
	binary.LittleEndian.PutUint64(p[0:], uint64(v))
	b.buf = append(b.buf, p[:]...)
}

func (b *podBuilder) addFloat(v float32) {
	b.writeHeader(4, spaTypeFloat)
	var p [8]byte
	binary.LittleEndian.PutUint32(p[0:], math.Float32bits(v))
	b.buf = append(b.buf, p[:]...)
}

func (b *podBuilder) addString(s string) {
	n := len(s) + 1 // NUL terminated.
	b.writeHeader(uint32(n), spaTypeString)
	b.buf = append(b.buf, s...)
	b.buf = append(b.buf, 0)
	b.pad(n)
}

func (b *podBuilder) addFd(index int64) {
	b.writeHeader(8, spaTypeFd)
	var p [8]byte
	binary.LittleEndian.PutUint64(p[0:], uint64(index))
	b.buf = append(b.buf, p[:]...)
}

// addPod appends a fully built child pod (header + payload).
func (b *podBuilder) addPod(pod []byte) {
	if pod == nil {
		b.addNone()
		return
	}
	b.buf = append(b.buf, pod...)
	b.pad(0)
}

// frame tracks a container (struct/object) under construction.
type podFrame struct {
	offset int // Offset of the container's size field.
}

func (b *podBuilder) pushStruct() podFrame {
	f := podFrame{offset: len(b.buf)}
	b.writeHeader(0, spaTypeStruct)
	return f
}

func (b *podBuilder) pushObject(objType, objID uint32) podFrame {
	f := podFrame{offset: len(b.buf)}
	b.writeHeader(8, spaTypeObject)
	var p [8]byte
	binary.LittleEndian.PutUint32(p[0:], objType)
	binary.LittleEndian.PutUint32(p[4:], objID)
	b.buf = append(b.buf, p[:]...)
	return f
}

// addProp writes an object property header (key + flags). The next value
// added becomes the property's value.
func (b *podBuilder) addProp(key, flags uint32) {
	var p [8]byte
	binary.LittleEndian.PutUint32(p[0:], key)
	binary.LittleEndian.PutUint32(p[4:], flags)
	b.buf = append(b.buf, p[:]...)
}

func (b *podBuilder) pop(f podFrame) {
	size := len(b.buf) - f.offset - 8
	binary.LittleEndian.PutUint32(b.buf[f.offset:], uint32(size))
}

// addChoiceEnumID writes a Choice pod of Id values: the default followed by
// the alternatives.
func (b *podBuilder) addChoiceEnumID(def uint32, alternatives ...uint32) {
	n := 1 + len(alternatives)
	payload := 8 + 8 + n*4 // choice header + child header + values... child values are 4 bytes each for Id.
	_ = payload
	f := podFrame{offset: len(b.buf)}
	b.writeHeader(0, spaTypeChoice)
	var hdr [16]byte
	binary.LittleEndian.PutUint32(hdr[0:], spaChoiceEnum)
	binary.LittleEndian.PutUint32(hdr[4:], 0) // flags
	binary.LittleEndian.PutUint32(hdr[8:], 4) // child size
	binary.LittleEndian.PutUint32(hdr[12:], spaTypeID)
	b.buf = append(b.buf, hdr[:]...)
	var v [4]byte
	binary.LittleEndian.PutUint32(v[:], def)
	b.buf = append(b.buf, v[:]...)
	binary.LittleEndian.PutUint32(v[:], def) // First alternative repeats the default.
	b.buf = append(b.buf, v[:]...)
	for _, a := range alternatives {
		binary.LittleEndian.PutUint32(v[:], a)
		b.buf = append(b.buf, v[:]...)
	}
	b.pop(f)
	b.pad(0)
}

// addChoiceRangeInt writes a Choice pod: default, min, max ints.
func (b *podBuilder) addChoiceRangeInt(def, minV, maxV int32) {
	f := podFrame{offset: len(b.buf)}
	b.writeHeader(0, spaTypeChoice)
	var hdr [16]byte
	binary.LittleEndian.PutUint32(hdr[0:], spaChoiceRange)
	binary.LittleEndian.PutUint32(hdr[4:], 0)
	binary.LittleEndian.PutUint32(hdr[8:], 4)
	binary.LittleEndian.PutUint32(hdr[12:], spaTypeInt)
	b.buf = append(b.buf, hdr[:]...)
	var v [4]byte
	for _, x := range []int32{def, minV, maxV} {
		binary.LittleEndian.PutUint32(v[:], uint32(x))
		b.buf = append(b.buf, v[:]...)
	}
	b.pop(f)
	b.pad(0)
}

/**************************************************************************
POD parsing
**************************************************************************/

// pod is a parsed POD value.
type pod struct {
	Type    uint32
	Payload []byte
}

// parsePod parses one pod from data, returning the pod and the number of
// bytes consumed (aligned).
func parsePod(data []byte) (pod, int, bool) {
	if len(data) < 8 {
		return pod{}, 0, false
	}
	size := binary.LittleEndian.Uint32(data[0:])
	typ := binary.LittleEndian.Uint32(data[4:])
	total := 8 + align8(int(size))
	if len(data) < 8+int(size) {
		return pod{}, 0, false
	}
	end := 8 + int(size)
	if total > len(data) {
		total = len(data)
	}
	return pod{Type: typ, Payload: data[8:end]}, total, true
}

// podParser iterates the fields of a struct pod payload.
type podParser struct {
	data []byte
}

func (p *podParser) next() (pod, bool) {
	v, n, ok := parsePod(p.data)
	if !ok {
		return pod{}, false
	}
	p.data = p.data[n:]
	return v, true
}

func (p *podParser) nextInt() (int32, bool) {
	v, ok := p.next()
	if !ok || (v.Type != spaTypeInt && v.Type != spaTypeID) || len(v.Payload) < 4 {
		return 0, false
	}
	return int32(binary.LittleEndian.Uint32(v.Payload)), true
}

func (p *podParser) nextLong() (int64, bool) {
	v, ok := p.next()
	if !ok || v.Type != spaTypeLong || len(v.Payload) < 8 {
		return 0, false
	}
	return int64(binary.LittleEndian.Uint64(v.Payload)), true
}

func (p *podParser) nextString() (string, bool) {
	v, ok := p.next()
	if !ok || v.Type != spaTypeString {
		return "", false
	}
	s := v.Payload
	// Strip the NUL terminator.
	for i, c := range s {
		if c == 0 {
			return string(s[:i]), true
		}
	}
	return string(s), true
}

func (p *podParser) nextFd() (int64, bool) {
	v, ok := p.next()
	if !ok || v.Type != spaTypeFd || len(v.Payload) < 8 {
		return 0, false
	}
	return int64(binary.LittleEndian.Uint64(v.Payload)), true
}

// structParser returns a parser over a struct pod's fields.
func (v pod) structParser() *podParser {
	return &podParser{data: v.Payload}
}

// objectProp is one property of an object pod.
type objectProp struct {
	Key   uint32
	Flags uint32
	Value pod
}

// parseObject parses an object pod payload into its type/id and properties.
func (v pod) parseObject() (objType, objID uint32, props []objectProp, ok bool) {
	if v.Type != spaTypeObject || len(v.Payload) < 8 {
		return 0, 0, nil, false
	}
	objType = binary.LittleEndian.Uint32(v.Payload[0:])
	objID = binary.LittleEndian.Uint32(v.Payload[4:])
	data := v.Payload[8:]
	for len(data) >= 16 {
		key := binary.LittleEndian.Uint32(data[0:])
		flags := binary.LittleEndian.Uint32(data[4:])
		value, n, pok := parsePod(data[8:])
		if !pok {
			break
		}
		props = append(props, objectProp{Key: key, Flags: flags, Value: value})
		data = data[8+n:]
	}
	return objType, objID, props, true
}

// valueInt extracts an int/id value from a pod, unwrapping single-value
// choices.
func (v pod) valueInt() (int32, bool) {
	switch v.Type {
	case spaTypeInt, spaTypeID:
		if len(v.Payload) >= 4 {
			return int32(binary.LittleEndian.Uint32(v.Payload)), true
		}
	case spaTypeChoice:
		if len(v.Payload) >= 16+4 {
			choiceType := binary.LittleEndian.Uint32(v.Payload[0:])
			childType := binary.LittleEndian.Uint32(v.Payload[12:])
			if (choiceType == spaChoiceNone || choiceType == spaChoiceEnum || choiceType == spaChoiceRange) &&
				(childType == spaTypeInt || childType == spaTypeID) {
				return int32(binary.LittleEndian.Uint32(v.Payload[16:])), true
			}
		}
	}
	return 0, false
}
