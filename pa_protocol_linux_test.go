//go:build linux && !android

package gominiaudio

import (
	"bytes"
	"testing"
)

// The expected byte sequences below are derived from PulseAudio's
// src/pulsecore/tagstruct.c: every value is a one byte tag followed by a
// big endian payload. Getting these wrong is silent on the wire (the
// daemon just reports a protocol error), so they are pinned here.
func TestPATagWriterWireFormat(t *testing.T) {
	tests := []struct {
		name string
		put  func(w *paTagWriter)
		want []byte
	}{
		{"u32", func(w *paTagWriter) { w.putU32(0x12345678) },
			[]byte{'L', 0x12, 0x34, 0x56, 0x78}},
		{"u8", func(w *paTagWriter) { w.putU8(7) },
			[]byte{'B', 0x07}},
		{"u64", func(w *paTagWriter) { w.putU64(1) },
			[]byte{'R', 0, 0, 0, 0, 0, 0, 0, 1}},
		{"usec", func(w *paTagWriter) { w.putUsec(1) },
			[]byte{'U', 0, 0, 0, 0, 0, 0, 0, 1}},
		{"string", func(w *paTagWriter) { w.putString("hi") },
			[]byte{'t', 'h', 'i', 0}},
		{"empty string", func(w *paTagWriter) { w.putString("") },
			[]byte{'t', 0}},
		{"null string", func(w *paTagWriter) { w.putNullString() },
			[]byte{'N'}},
		{"true", func(w *paTagWriter) { w.putBool(true) },
			[]byte{'1'}},
		{"false", func(w *paTagWriter) { w.putBool(false) },
			[]byte{'0'}},
		{"arbitrary", func(w *paTagWriter) { w.putArbitrary([]byte{0xAA, 0xBB}) },
			[]byte{'x', 0, 0, 0, 2, 0xAA, 0xBB}},
		{"sample spec", func(w *paTagWriter) { w.putSampleSpec(paSampleS16LE, 2, 48000) },
			[]byte{'a', 3, 2, 0x00, 0x00, 0xBB, 0x80}},
		{"channel map", func(w *paTagWriter) { w.putChannelMap([]uint8{1, 2}) },
			[]byte{'m', 2, 1, 2}},
		{"cvolume", func(w *paTagWriter) { w.putCVolume([]uint32{paVolumeNorm}) },
			[]byte{'v', 1, 0x00, 0x01, 0x00, 0x00}},
		{
			// Proplist values are stored with their NUL terminator, and the
			// length counts it: "b" is two bytes on the wire.
			"proplist",
			func(w *paTagWriter) { w.putProplist([][2]string{{"a", "b"}}) },
			[]byte{
				'P',
				't', 'a', 0,
				'L', 0, 0, 0, 2,
				'x', 0, 0, 0, 2, 'b', 0,
				'N',
			},
		},
		{"empty proplist", func(w *paTagWriter) { w.putProplist(nil) },
			[]byte{'P', 'N'}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var w paTagWriter
			tc.put(&w)
			if !bytes.Equal(w.buf, tc.want) {
				t.Errorf("got % x, want % x", w.buf, tc.want)
			}
		})
	}
}

// TestPATagRoundTrip checks the reader against the writer for every type
// the backend exchanges, in one stream, so field ordering is covered too.
func TestPATagRoundTrip(t *testing.T) {
	var w paTagWriter
	w.putU32(4242)
	w.putString("sink-name")
	w.putNullString()
	w.putBool(true)
	w.putBool(false)
	w.putU8(9)
	w.putSampleSpec(paSampleFloat32LE, 6, 44100)
	w.putChannelMap([]uint8{1, 2, 3, 7, 10, 11})
	w.putCVolume([]uint32{paVolumeNorm, paVolumeNorm})
	w.putUsec(123456)
	w.putArbitrary([]byte{1, 2, 3})
	w.putProplist([][2]string{{"application.name", "gominiaudio"}, {"media.role", "music"}})
	w.putU32(7)

	r := &paTagReader{buf: w.buf}

	if v, err := r.getU32(); err != nil || v != 4242 {
		t.Fatalf("u32: got %v, %v", v, err)
	}
	if s, ok, err := r.getString(); err != nil || !ok || s != "sink-name" {
		t.Fatalf("string: got %q ok=%v err=%v", s, ok, err)
	}
	if s, ok, err := r.getString(); err != nil || ok || s != "" {
		t.Fatalf("null string: got %q ok=%v err=%v", s, ok, err)
	}
	if b, err := r.getBool(); err != nil || !b {
		t.Fatalf("bool true: got %v, %v", b, err)
	}
	if b, err := r.getBool(); err != nil || b {
		t.Fatalf("bool false: got %v, %v", b, err)
	}
	if v, err := r.getU8(); err != nil || v != 9 {
		t.Fatalf("u8: got %v, %v", v, err)
	}
	format, channels, rate, err := r.getSampleSpec()
	if err != nil || format != paSampleFloat32LE || channels != 6 || rate != 44100 {
		t.Fatalf("sample spec: got %v %v %v, %v", format, channels, rate, err)
	}
	cmap, err := r.getChannelMap()
	if err != nil || !bytes.Equal(cmap, []byte{1, 2, 3, 7, 10, 11}) {
		t.Fatalf("channel map: got %v, %v", cmap, err)
	}
	vol, err := r.getCVolume()
	if err != nil || len(vol) != 2 || vol[0] != paVolumeNorm {
		t.Fatalf("cvolume: got %v, %v", vol, err)
	}
	if v, err := r.getU64(); err != nil || v != 123456 {
		t.Fatalf("usec: got %v, %v", v, err)
	}
	arb, err := r.getArbitrary()
	if err != nil || !bytes.Equal(arb, []byte{1, 2, 3}) {
		t.Fatalf("arbitrary: got %v, %v", arb, err)
	}
	if err := r.skipProplist(); err != nil {
		t.Fatalf("skipProplist: %v", err)
	}
	// Reaching this value proves the proplist was skipped by exactly the
	// right number of bytes.
	if v, err := r.getU32(); err != nil || v != 7 {
		t.Fatalf("trailing u32 after proplist: got %v, %v", v, err)
	}
	if !r.eof() {
		t.Errorf("expected end of tagstruct, %d bytes left", len(r.buf)-r.pos)
	}
}

// TestPATagReaderTruncated checks that a short buffer is reported rather
// than panicking, since packets arrive from another process.
func TestPATagReaderTruncated(t *testing.T) {
	var w paTagWriter
	w.putU32(1)
	w.putString("truncated")

	for n := 0; n < len(w.buf); n++ {
		r := &paTagReader{buf: w.buf[:n]}
		// Any combination of these may fail; none may panic.
		_, _ = r.getU32()
		_, _, _ = r.getString()
	}
}

// TestPATagReaderTypeMismatch checks that the reader refuses a value of the
// wrong type instead of silently misparsing the rest of the stream.
func TestPATagReaderTypeMismatch(t *testing.T) {
	var w paTagWriter
	w.putString("not a number")
	r := &paTagReader{buf: w.buf}
	if _, err := r.getU32(); err == nil {
		t.Error("expected an error reading a string as u32")
	}
}

// TestPAFormatMapping checks the format mapping round trips for every
// format both sides share.
func TestPAFormatMapping(t *testing.T) {
	for _, f := range []Format{FormatU8, FormatS16, FormatS24, FormatS32, FormatF32} {
		pa := paFormatFromMA(f)
		if pa == paSampleInvalid {
			t.Errorf("%v has no PulseAudio equivalent", f)
			continue
		}
		if got := paFormatToMA(pa); got != f {
			t.Errorf("%v round tripped to %v", f, got)
		}
	}
	if paFormatFromMA(FormatUnknown) != paSampleInvalid {
		t.Error("FormatUnknown should not map to a PulseAudio format")
	}
}

// TestPAChannelMapFor checks a map is produced for every supported channel
// count, with one position per channel.
func TestPAChannelMapFor(t *testing.T) {
	for ch := uint32(1); ch <= 8; ch++ {
		m := paChannelMapFor(ch)
		if uint32(len(m)) != ch {
			t.Errorf("%d channels: got %d positions", ch, len(m))
		}
	}
	stereo := paChannelMapFor(2)
	if stereo[0] != paChannelFrontLeft || stereo[1] != paChannelFrontRight {
		t.Errorf("stereo map = %v, want front left/right", stereo)
	}
	if mono := paChannelMapFor(1); mono[0] != paChannelMono {
		t.Errorf("mono map = %v, want mono", mono)
	}
}

// TestPACommandOpcodes pins every command ordinal used by the backend.
//
// These are positions in an unnumbered C enum, so a single miscount shifts
// a whole run of them. That failure is nasty on the wire: the daemon reads
// a different command, fails its end-of-payload check and drops the
// connection, which surfaces only as an I/O error much later. An earlier
// revision had exactly that bug in the three record-stream commands.
func TestPACommandOpcodes(t *testing.T) {
	// Position of each name in PA_COMMAND_* (src/pulsecore/native-common.h).
	want := map[string]uint32{
		"ERROR":                  0,
		"REPLY":                  2,
		"CREATE_PLAYBACK_STREAM": 3,
		"DELETE_PLAYBACK_STREAM": 4,
		"CREATE_RECORD_STREAM":   5,
		"DELETE_RECORD_STREAM":   6,
		"AUTH":                   8,
		"SET_CLIENT_NAME":        9,
		"GET_SERVER_INFO":        20,
		"GET_SINK_INFO_LIST":     22,
		"GET_SOURCE_INFO_LIST":   24,
		"CORK_PLAYBACK_STREAM":   41,
		"FLUSH_PLAYBACK_STREAM":  42,
		"GET_RECORD_LATENCY":     57,
		"CORK_RECORD_STREAM":     58,
		"FLUSH_RECORD_STREAM":    59,
		"PREBUF_PLAYBACK_STREAM": 60,
		"REQUEST":                61,
		"OVERFLOW":               62,
		"UNDERFLOW":              63,
		"PLAYBACK_STREAM_KILLED": 64,
		"RECORD_STREAM_KILLED":   65,
		"SUBSCRIBE_EVENT":        66,
	}
	got := map[string]uint32{
		"ERROR":                  paCommandError,
		"REPLY":                  paCommandReply,
		"CREATE_PLAYBACK_STREAM": paCommandCreatePlaybackStream,
		"DELETE_PLAYBACK_STREAM": paCommandDeletePlaybackStream,
		"CREATE_RECORD_STREAM":   paCommandCreateRecordStream,
		"DELETE_RECORD_STREAM":   paCommandDeleteRecordStream,
		"AUTH":                   paCommandAuth,
		"SET_CLIENT_NAME":        paCommandSetClientName,
		"GET_SERVER_INFO":        paCommandGetServerInfo,
		"GET_SINK_INFO_LIST":     paCommandGetSinkInfoList,
		"GET_SOURCE_INFO_LIST":   paCommandGetSourceInfoList,
		"CORK_PLAYBACK_STREAM":   paCommandCorkPlaybackStream,
		"FLUSH_PLAYBACK_STREAM":  paCommandFlushPlaybackStream,
		"GET_RECORD_LATENCY":     paCommandGetRecordLatency,
		"CORK_RECORD_STREAM":     paCommandCorkRecordStream,
		"FLUSH_RECORD_STREAM":    paCommandFlushRecordStream,
		"PREBUF_PLAYBACK_STREAM": paCommandPrebufPlaybackStream,
		"REQUEST":                paCommandRequest,
		"OVERFLOW":               paCommandOverflow,
		"UNDERFLOW":              paCommandUnderflow,
		"PLAYBACK_STREAM_KILLED": paCommandPlaybackStreamKilled,
		"RECORD_STREAM_KILLED":   paCommandRecordStreamKilled,
		"SUBSCRIBE_EVENT":        paCommandSubscribeEvent,
	}
	for name, w := range want {
		if g := got[name]; g != w {
			t.Errorf("PA_COMMAND_%s = %d, want %d", name, g, w)
		}
	}
}

// TestPASampleFormatOrdinals pins the pa_sample_format_t ordinals, which
// have the same miscount hazard as the commands.
func TestPASampleFormatOrdinals(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  uint8
		want uint8
	}{
		{"U8", paSampleU8, 0},
		{"ALAW", paSampleALaw, 1},
		{"ULAW", paSampleULaw, 2},
		{"S16LE", paSampleS16LE, 3},
		{"S16BE", paSampleS16BE, 4},
		{"FLOAT32LE", paSampleFloat32LE, 5},
		{"FLOAT32BE", paSampleFloat32BE, 6},
		{"S32LE", paSampleS32LE, 7},
		{"S32BE", paSampleS32BE, 8},
		{"S24LE", paSampleS24LE, 9},
		{"S24BE", paSampleS24BE, 10},
		{"S24_32LE", paSampleS24_32LE, 11},
		{"S24_32BE", paSampleS24_32BE, 12},
	} {
		if tc.got != tc.want {
			t.Errorf("PA_SAMPLE_%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

// TestPAChannelPositionOrdinals pins the pa_channel_position_t ordinals.
func TestPAChannelPositionOrdinals(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  uint8
		want uint8
	}{
		{"MONO", paChannelMono, 0},
		{"FRONT_LEFT", paChannelFrontLeft, 1},
		{"FRONT_RIGHT", paChannelFrontRight, 2},
		{"FRONT_CENTER", paChannelFrontCenter, 3},
		{"REAR_CENTER", paChannelRearCenter, 4},
		{"REAR_LEFT", paChannelRearLeft, 5},
		{"REAR_RIGHT", paChannelRearRight, 6},
		{"LFE", paChannelLFE, 7},
		{"FRONT_LEFT_OF_CENTER", paChannelFrontLeftOfCenter, 8},
		{"FRONT_RIGHT_OF_CENTER", paChannelFrontRightOfCentr, 9},
		{"SIDE_LEFT", paChannelSideLeft, 10},
		{"SIDE_RIGHT", paChannelSideRight, 11},
		{"AUX0", paChannelAux0, 12},
	} {
		if tc.got != tc.want {
			t.Errorf("PA_CHANNEL_POSITION_%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}
