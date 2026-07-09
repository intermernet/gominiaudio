package gominiaudio

import (
	"bytes"
	"encoding/binary"
	"math"
	"sync"
	"testing"
	"time"
)

func TestFormatSizes(t *testing.T) {
	cases := map[Format]int{
		FormatU8: 1, FormatS16: 2, FormatS24: 3, FormatS32: 4, FormatF32: 4, FormatUnknown: 0,
	}
	for f, want := range cases {
		if got := f.SizeInBytes(); got != want {
			t.Errorf("%v.SizeInBytes() = %d, want %d", f, got, want)
		}
	}
	if FrameSizeInBytes(FormatS16, 2) != 4 {
		t.Error("FrameSizeInBytes(S16, 2) != 4")
	}
}

func TestPCMConvertRoundTrip(t *testing.T) {
	// s16 -> f32 -> s16 must round-trip near-exactly (within 1 LSB).
	src := make([]byte, 6*2)
	vals := []int16{0, 1, -1, 32767, -32768, 12345}
	for i, v := range vals {
		src[i*2] = byte(v)
		src[i*2+1] = byte(v >> 8)
	}
	f32buf := make([]byte, 6*4)
	if err := PCMConvert(f32buf, FormatF32, src, FormatS16, 6, DitherModeNone); err != nil {
		t.Fatal(err)
	}
	back := make([]byte, 6*2)
	if err := PCMConvert(back, FormatS16, f32buf, FormatF32, 6, DitherModeNone); err != nil {
		t.Fatal(err)
	}
	for i, want := range vals {
		got := int16(uint16(back[i*2]) | uint16(back[i*2+1])<<8)
		diff := int32(got) - int32(want)
		if diff < -1 || diff > 1 {
			t.Errorf("sample %d: got %d, want %d", i, got, want)
		}
	}
}

func TestPCMConvertIntegerShifts(t *testing.T) {
	// u8 -> s16 must be (x-128)<<8, matching miniaudio.
	src := []byte{0, 128, 255}
	dst := make([]byte, 3*2)
	if err := PCMConvert(dst, FormatS16, src, FormatU8, 3, DitherModeNone); err != nil {
		t.Fatal(err)
	}
	want := []int16{-32768, 0, 32512} // (255-128)<<8 = 32512.
	for i, w := range want {
		got := int16(uint16(dst[i*2]) | uint16(dst[i*2+1])<<8)
		if got != w {
			t.Errorf("sample %d: got %d, want %d", i, got, w)
		}
	}

	// s24 round trip through s32.
	s24 := []byte{0x01, 0x02, 0x03}
	s32 := make([]byte, 4)
	if err := PCMConvert(s32, FormatS32, s24, FormatS24, 1, DitherModeNone); err != nil {
		t.Fatal(err)
	}
	back := make([]byte, 3)
	if err := PCMConvert(back, FormatS24, s32, FormatS32, 1, DitherModeNone); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(s24, back) {
		t.Errorf("s24->s32->s24 mismatch: %v vs %v", s24, back)
	}
}

func TestPCMConvertF32Integer(t *testing.T) {
	// f32 -> {s16,s24,s32} -> f32 must round-trip within one LSB of the
	// target format. Exercises the specialized no-dither conversion paths
	// (and, under GOEXPERIMENT=simd, the SIMD s16 kernels).
	vals := []float32{0, 0.5, -0.5, 1, -1, 0.123456, -0.789012, 0.999969}
	n := len(vals)
	srcF := make([]byte, n*4)
	copy(srcF, f32ToBytes(vals))

	type tc struct {
		format Format
		bytes  int
		tol    float64 // Round-trip tolerance in [-1,1] units.
	}
	// Tolerance is 2 LSB: miniaudio scales asymmetrically (multiply by
	// 32767 on the way down, divide by 32768 on the way back), so a
	// round-trip can drift up to nearly two quantization steps.
	for _, c := range []tc{
		{FormatS16, 2, 2.0 / 32767},
		{FormatS24, 3, 2.0 / 8388607},
		{FormatS32, 4, 1.0 / 2147483647 * 8}, // f32 mantissa is the limit here, not s32.
	} {
		mid := make([]byte, n*c.bytes)
		if err := PCMConvert(mid, c.format, srcF, FormatF32, uint64(n), DitherModeNone); err != nil {
			t.Fatal(err)
		}
		back := make([]byte, n*4)
		if err := PCMConvert(back, FormatF32, mid, c.format, uint64(n), DitherModeNone); err != nil {
			t.Fatal(err)
		}
		got := bytesToF32(back)
		for i, want := range vals {
			if math.Abs(float64(got[i]-want)) > c.tol {
				t.Errorf("%v: sample %d round-trip %g -> %g (tol %g)", c.format, i, want, got[i], c.tol)
			}
		}
	}

	// Full-scale must clamp, not wrap: +1.0 -> max positive, -1.0 -> min.
	over := []float32{2.0, -2.0}
	for _, c := range []struct {
		format Format
		bytes  int
	}{{FormatS16, 2}, {FormatS24, 3}, {FormatS32, 4}} {
		dst := make([]byte, len(over)*c.bytes)
		if err := PCMConvert(dst, c.format, f32ToBytes(over), FormatF32, uint64(len(over)), DitherModeNone); err != nil {
			t.Fatal(err)
		}
		var pos, neg int32
		switch c.format {
		case FormatS16:
			pos = int32(int16(uint16(dst[0]) | uint16(dst[1])<<8))
			neg = int32(int16(uint16(dst[2]) | uint16(dst[3])<<8))
		case FormatS24:
			pos = readS24(dst[0:])
			neg = readS24(dst[3:])
		case FormatS32:
			pos = int32(binary.LittleEndian.Uint32(dst[0:]))
			neg = int32(binary.LittleEndian.Uint32(dst[4:]))
		}
		if pos <= 0 || neg >= 0 {
			t.Errorf("%v: overflow not clamped: pos=%d neg=%d", c.format, pos, neg)
		}
	}
}

func TestChannelMapStandard(t *testing.T) {
	m := ChannelMapInitStandard(StandardChannelMapMicrosoft, 2)
	if m[0] != ChannelFrontLeft || m[1] != ChannelFrontRight {
		t.Errorf("stereo map wrong: %v", m)
	}
	m6 := ChannelMapInitStandard(StandardChannelMapMicrosoft, 6)
	if m6[3] != ChannelLFE {
		t.Errorf("5.1 LFE position wrong: %v", m6)
	}
	if !ChannelMapIsEqual(nil, m, 2) {
		t.Error("nil map should equal default stereo map")
	}
}

func TestChannelConverterMonoToStereo(t *testing.T) {
	cc, err := NewChannelConverter(ChannelConverterConfigInit(FormatF32, 1, nil, 2, nil, ChannelMixModeDefault))
	if err != nil {
		t.Fatal(err)
	}
	src := []float32{0.5, -0.25}
	dst := make([]float32, 4)
	if err := cc.ProcessF32(dst, src, 2); err != nil {
		t.Fatal(err)
	}
	want := []float32{0.5, 0.5, -0.25, -0.25}
	for i := range want {
		if dst[i] != want[i] {
			t.Errorf("dst[%d] = %f, want %f", i, dst[i], want[i])
		}
	}
}

func TestChannelConverterStereoToMono(t *testing.T) {
	cc, err := NewChannelConverter(ChannelConverterConfigInit(FormatF32, 2, nil, 1, nil, ChannelMixModeDefault))
	if err != nil {
		t.Fatal(err)
	}
	src := []float32{1, 0, -0.5, 0.5}
	dst := make([]float32, 2)
	if err := cc.ProcessF32(dst, src, 2); err != nil {
		t.Fatal(err)
	}
	if dst[0] != 0.5 || dst[1] != 0 {
		t.Errorf("mono mix wrong: %v", dst)
	}
}

func TestLinearResamplerRatio(t *testing.T) {
	r, err := NewLinearResampler(LinearResamplerConfigInit(FormatF32, 1, 44100, 48000))
	if err != nil {
		t.Fatal(err)
	}
	in := make([]float32, 4410) // 100ms at 44.1k.
	for i := range in {
		in[i] = float32(math.Sin(2 * math.Pi * 440 * float64(i) / 44100))
	}
	out := make([]float32, 4800+16)
	fin, fout, err := r.ProcessF32(out, in, uint64(len(in)), uint64(len(out)))
	if err != nil {
		t.Fatal(err)
	}
	if fin == 0 || fout == 0 {
		t.Fatal("no frames processed")
	}
	// Expect roughly 4800 output frames for 4410 input frames.
	ratio := float64(fout) / float64(fin)
	if ratio < 1.06 || ratio > 1.12 {
		t.Errorf("resample ratio %f out of range (in=%d out=%d)", ratio, fin, fout)
	}
}

func TestLinearResamplerChunkInvariance(t *testing.T) {
	// Resampling a stream in one large call must produce bit-identical
	// output to processing it in small chunks: filter state and timing must
	// carry across call boundaries. Covers both the downsampling (input
	// block LPF) and upsampling (output block LPF) paths.
	cases := []struct{ rateIn, rateOut uint32 }{
		{48000, 44100}, // Downsampling.
		{44100, 48000}, // Upsampling.
	}
	for _, tc := range cases {
		in := make([]float32, 4410*2)
		for i := 0; i < 4410; i++ {
			v := float32(math.Sin(2*math.Pi*440*float64(i)/float64(tc.rateIn))) +
				0.25*float32(math.Sin(2*math.Pi*9000*float64(i)/float64(tc.rateIn)))
			in[i*2] = v
			in[i*2+1] = -v
		}

		r1, err := NewLinearResampler(LinearResamplerConfigInit(FormatF32, 2, tc.rateIn, tc.rateOut))
		if err != nil {
			t.Fatal(err)
		}
		r2, err := NewLinearResampler(LinearResamplerConfigInit(FormatF32, 2, tc.rateIn, tc.rateOut))
		if err != nil {
			t.Fatal(err)
		}

		// One big call.
		outBig := make([]float32, 6000*2)
		_, wholeOut, err := r1.ProcessF32(outBig, in, 4410, 6000)
		if err != nil {
			t.Fatal(err)
		}

		// Many small calls with awkward chunk sizes.
		outChunked := make([]float32, 6000*2)
		var consumedIn, producedOut uint64
		chunkSizes := []uint64{7, 64, 100, 3, 501, 128}
		ci := 0
		for consumedIn < 4410 && producedOut < wholeOut {
			chunkIn := chunkSizes[ci%len(chunkSizes)]
			ci++
			if consumedIn+chunkIn > 4410 {
				chunkIn = 4410 - consumedIn
			}
			fin, fout, err := r2.ProcessF32(
				outChunked[producedOut*2:],
				in[consumedIn*2:],
				chunkIn, wholeOut-producedOut)
			if err != nil {
				t.Fatal(err)
			}
			consumedIn += fin
			producedOut += fout
			if fin == 0 && fout == 0 {
				break
			}
		}

		if producedOut != wholeOut {
			t.Fatalf("%d->%d: chunked produced %d frames, whole call produced %d",
				tc.rateIn, tc.rateOut, producedOut, wholeOut)
		}
		for i := 0; i < int(wholeOut)*2; i++ {
			if outBig[i] != outChunked[i] {
				t.Fatalf("%d->%d: sample %d differs: whole=%g chunked=%g",
					tc.rateIn, tc.rateOut, i, outBig[i], outChunked[i])
			}
		}
	}
}

func TestDataConverterPassthrough(t *testing.T) {
	cfg := DataConverterConfigInit(FormatS16, FormatS16, 2, 2, 48000, 48000)
	dc, err := NewDataConverter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	src := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	dst := make([]byte, 8)
	fin, fout, err := dc.Process(dst, src, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if fin != 2 || fout != 2 || !bytes.Equal(src, dst) {
		t.Errorf("passthrough failed: in=%d out=%d dst=%v", fin, fout, dst)
	}
}

func TestDataConverterFull(t *testing.T) {
	// s16 mono 44.1k -> f32 stereo 48k.
	cfg := DataConverterConfigInit(FormatS16, FormatF32, 1, 2, 44100, 48000)
	dc, err := NewDataConverter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	in := make([]byte, 441*2)
	out := make([]byte, 500*2*4)
	fin, fout, err := dc.Process(out, in, 441, 500)
	if err != nil {
		t.Fatal(err)
	}
	if fin == 0 || fout == 0 {
		t.Errorf("no progress: in=%d out=%d", fin, fout)
	}
}

func TestRingBuffer(t *testing.T) {
	rb, err := NewRB(64, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rb.AvailableRead() != 0 || rb.AvailableWrite() != 64 {
		t.Fatalf("fresh rb: read=%d write=%d", rb.AvailableRead(), rb.AvailableWrite())
	}
	w := rb.AcquireWrite(16)
	for i := range w {
		w[i] = byte(i)
	}
	if err := rb.CommitWrite(16); err != nil {
		t.Fatal(err)
	}
	if rb.AvailableRead() != 16 {
		t.Fatalf("available read = %d", rb.AvailableRead())
	}
	r := rb.AcquireRead(16)
	for i := range r {
		if r[i] != byte(i) {
			t.Fatalf("read mismatch at %d", i)
		}
	}
	if err := rb.CommitRead(16); err != nil {
		t.Fatal(err)
	}

	// Wraparound.
	for k := 0; k < 10; k++ {
		w := rb.AcquireWrite(48)
		if err := rb.CommitWrite(uint32(len(w))); err != nil {
			t.Fatal(err)
		}
		r := rb.AcquireRead(48)
		if err := rb.CommitRead(uint32(len(r))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPCMRB(t *testing.T) {
	rb, err := NewPCMRB(FormatS16, 2, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	buf := rb.AcquireWrite(10)
	if len(buf) != 10*4 {
		t.Fatalf("acquire write returned %d bytes", len(buf))
	}
	if err := rb.CommitWrite(10); err != nil {
		t.Fatal(err)
	}
	if rb.AvailableRead() != 10 {
		t.Fatalf("available read = %d frames", rb.AvailableRead())
	}
}

func TestWAVRoundTrip(t *testing.T) {
	var f writeSeekBuffer
	enc, err := NewWAVEncoder(&f, FormatS16, 2, 44100)
	if err != nil {
		t.Fatal(err)
	}
	frames := make([]byte, 1000*4)
	for i := 0; i < 1000; i++ {
		v := int16(math.Sin(2*math.Pi*440*float64(i)/44100) * 10000)
		frames[i*4] = byte(v)
		frames[i*4+1] = byte(v >> 8)
		frames[i*4+2] = byte(v)
		frames[i*4+3] = byte(v >> 8)
	}
	if _, err := enc.WritePCMFrames(frames, 1000); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}

	dec, err := NewDecoderMemory(f.buf, nil)
	if err != nil {
		t.Fatal(err)
	}
	format, channels, rate, _, _ := dec.DataFormat()
	if format != FormatS16 || channels != 2 || rate != 44100 {
		t.Fatalf("decoded format %v/%d/%d", format, channels, rate)
	}
	length, err := dec.Length()
	if err != nil || length != 1000 {
		t.Fatalf("length = %d (%v)", length, err)
	}
	got := make([]byte, 1000*4)
	read, err := dec.Read(got, 1000)
	if read != 1000 {
		t.Fatalf("read %d frames (%v)", read, err)
	}
	if !bytes.Equal(frames, got) {
		t.Error("decoded data mismatch")
	}

	// Seek and re-read.
	if err := dec.Seek(500); err != nil {
		t.Fatal(err)
	}
	read, _ = dec.Read(got, 1000)
	if read != 500 {
		t.Fatalf("read after seek = %d", read)
	}
}

func TestDecoderFormatConversion(t *testing.T) {
	var f writeSeekBuffer
	enc, _ := NewWAVEncoder(&f, FormatS16, 1, 22050)
	frames := make([]byte, 2205*2)
	enc.WritePCMFrames(frames, 2205)
	enc.Close()

	cfg := DecoderConfigInit(FormatF32, 2, 44100)
	dec, err := NewDecoderMemory(f.buf, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	format, channels, rate, _, _ := dec.DataFormat()
	if format != FormatF32 || channels != 2 || rate != 44100 {
		t.Fatalf("output format %v/%d/%d", format, channels, rate)
	}
	out := make([]byte, 8192*8)
	total := uint64(0)
	for {
		read, err := dec.Read(out, 8192)
		total += read
		if err == ErrAtEnd {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if read == 0 {
			break
		}
	}
	// 2205 frames at 22050 -> ~4410 at 44100.
	if total < 4000 || total > 4800 {
		t.Errorf("converted frame count = %d, want ~4410", total)
	}
}

func TestWaveform(t *testing.T) {
	w, err := NewWaveform(WaveformConfigInit(FormatF32, 1, 48000, WaveformTypeSine, 1.0, 480))
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 100*4)
	if _, err := w.Read(buf, 100); err != nil {
		t.Fatal(err)
	}
	s := bytesToF32(buf)
	if s[0] != 0 {
		t.Errorf("sine should start at 0, got %f", s[0])
	}
	// 480Hz at 48kHz has a period of 100 samples; sample 25 is sin(pi/2)=1.
	if math.Abs(float64(s[25])-1) > 1e-3 {
		t.Errorf("s[25] = %f, want ~1", s[25])
	}
}

func TestNoise(t *testing.T) {
	n, err := NewNoise(NoiseConfigInit(FormatF32, 2, NoiseTypeWhite, 0, 0.5))
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1000*2*4)
	if _, err := n.Read(buf, 1000); err != nil {
		t.Fatal(err)
	}
	s := bytesToF32(buf)
	var maxAbs float64
	nonZero := false
	for _, v := range s {
		a := math.Abs(float64(v))
		if a > maxAbs {
			maxAbs = a
		}
		if v != 0 {
			nonZero = true
		}
	}
	if !nonZero {
		t.Error("noise produced silence")
	}
	if maxAbs > 0.5001 {
		t.Errorf("white noise exceeded amplitude: %f", maxAbs)
	}

	for _, typ := range []NoiseType{NoiseTypePink, NoiseTypeBrownian} {
		n2, err := NewNoise(NoiseConfigInit(FormatF32, 1, typ, 0, 1))
		if err != nil {
			t.Fatal(err)
		}
		buf2 := make([]byte, 48000*4)
		n2.Read(buf2, 48000)
		s2 := bytesToF32(buf2)
		var m float64
		for _, v := range s2 {
			if a := math.Abs(float64(v)); a > m {
				m = a
			}
		}
		if m == 0 {
			t.Errorf("noise type %d produced silence", typ)
		}
		if m > 2 {
			t.Errorf("noise type %d unbounded: max %f", typ, m)
		}
	}
}

func TestDataSourceControllerLooping(t *testing.T) {
	data := make([]byte, 100*4)
	s := bytesToF32(data)
	for i := range s {
		s[i] = float32(i)
	}
	ab, err := NewAudioBufferRef(FormatF32, 1, data, 100)
	if err != nil {
		t.Fatal(err)
	}
	ctrl := NewDataSourceController(ab)
	_ = ctrl.SetLooping(true)

	out := make([]byte, 250*4)
	read, err := ctrl.Read(out, 250)
	if err != nil {
		t.Fatal(err)
	}
	if read != 250 {
		t.Fatalf("looped read = %d, want 250", read)
	}
	outF := bytesToF32(out)
	if outF[100] != 0 || outF[200] != 0 {
		t.Errorf("loop boundary values wrong: %f %f", outF[100], outF[200])
	}
}

func TestFilterStability(t *testing.T) {
	lpf, err := NewLPF(LPFConfigInit(FormatF32, 1, 48000, 1000, 4))
	if err != nil {
		t.Fatal(err)
	}
	in := make([]float32, 48000)
	for i := range in {
		in[i] = float32(math.Sin(2*math.Pi*100*float64(i)/48000)) + float32(math.Sin(2*math.Pi*10000*float64(i)/48000))
	}
	out := make([]float32, len(in))
	if err := lpf.ProcessF32(out, in, uint64(len(in))); err != nil {
		t.Fatal(err)
	}
	// The 10kHz component should be strongly attenuated; check bounded output.
	for i, v := range out {
		if math.IsNaN(float64(v)) || math.Abs(float64(v)) > 2.5 {
			t.Fatalf("unstable filter output at %d: %f", i, v)
		}
	}
	// RMS of the last half should be near the 100Hz component alone (~0.707).
	var sum float64
	for _, v := range out[24000:] {
		sum += float64(v) * float64(v)
	}
	rms := math.Sqrt(sum / 24000)
	if rms < 0.5 || rms > 0.9 {
		t.Errorf("LPF rms = %f, want ~0.707", rms)
	}
}

func TestNodeGraphAndEngine(t *testing.T) {
	cfg := EngineConfigInit()
	cfg.NoDevice = true
	cfg.Channels = 2
	cfg.SampleRate = 48000
	e, err := NewEngine(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Uninit()

	// A sine data source as a sound.
	w, err := NewWaveform(WaveformConfigInit(FormatF32, 2, 48000, WaveformTypeSine, 0.5, 440))
	if err != nil {
		t.Fatal(err)
	}
	snd, err := e.NewSoundFromDataSource(w, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer snd.Uninit()
	if err := snd.Start(); err != nil {
		t.Fatal(err)
	}

	out := make([]float32, 480*2)
	read, err := e.ReadPCMFrames(out, 480)
	if err != nil {
		t.Fatal(err)
	}
	if read != 480 {
		t.Fatalf("read %d frames", read)
	}
	nonZero := false
	for _, v := range out {
		if v != 0 {
			nonZero = true
			break
		}
	}
	if !nonZero {
		t.Error("engine produced silence with a playing sound")
	}

	// Volume control.
	snd.Stop()
	e.ReadPCMFrames(out, 480) // Flush.
	e.ReadPCMFrames(out, 480)
	allZero := true
	for _, v := range out {
		if v != 0 {
			allZero = false
			break
		}
	}
	if !allZero {
		t.Error("stopped sound still produced audio")
	}
}

func TestNodeGraphConcurrentControl(t *testing.T) {
	// Hammer the control plane (attach/detach/state/volume) from multiple
	// goroutines while the graph is being read. Run with -race; verifies the
	// lock-free control plane does not corrupt topology or block reads.
	g, err := NewNodeGraph(NodeGraphConfigInit(2))
	if err != nil {
		t.Fatal(err)
	}

	const numSources = 8
	sources := make([]*DataSourceNode, numSources)
	for i := range sources {
		w, _ := NewWaveform(WaveformConfigInit(FormatF32, 2, 48000, WaveformTypeSine, 0.1, float64(200+i*100)))
		n, err := g.NewDataSourceNode(DataSourceNodeConfigInit(w))
		if err != nil {
			t.Fatal(err)
		}
		sources[i] = n
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i, n := range sources {
		wg.Add(1)
		go func(i int, n *DataSourceNode) {
			defer wg.Done()
			for j := 0; ; j++ {
				select {
				case <-stop:
					return
				default:
				}
				switch j % 4 {
				case 0:
					_ = n.AttachOutputBus(0, g.Endpoint(), 0)
				case 1:
					_ = n.SetOutputBusVolume(0, float32(j%10)/10)
				case 2:
					_ = n.SetState(NodeState(j % 2))
				case 3:
					_ = n.DetachOutputBus(0)
				}
			}
		}(i, n)
	}

	out := make([]float32, 480*2)
	for i := 0; i < 200; i++ {
		if _, err := g.ReadPCMFrames(out, 480); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()

	// The graph must still be functional afterwards.
	for _, n := range sources {
		_ = n.DetachOutputBus(0)
	}
	if _, err := g.ReadPCMFrames(out, 480); err != nil {
		t.Fatal(err)
	}
}

func TestEngineSoundVolumePanPitch(t *testing.T) {
	cfg := EngineConfigInit()
	cfg.NoDevice = true
	cfg.Channels = 2
	cfg.SampleRate = 48000
	e, err := NewEngine(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Uninit()

	w, _ := NewWaveform(WaveformConfigInit(FormatF32, 2, 48000, WaveformTypeSquare, 1.0, 100))
	snd, err := e.NewSoundFromDataSource(w, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	snd.SetVolume(0.25)
	snd.SetPitch(2.0)
	snd.SetPan(0.5)
	if snd.Volume() != 0.25 || snd.Pitch() != 2.0 || snd.Pan() != 0.5 {
		t.Error("sound parameter roundtrip failed")
	}
	snd.Start()

	out := make([]float32, 4800*2)
	if _, err := e.ReadPCMFrames(out, 4800); err != nil {
		t.Fatal(err)
	}
	// Skip the gain smoothing window (8ms = 384 frames) before measuring.
	steady := out[1000*2:]
	maxAbs := maxAbsF32(steady)
	if maxAbs == 0 {
		t.Fatal("no audio produced")
	}
	if maxAbs > 0.3 {
		t.Errorf("volume not applied: max %f", maxAbs)
	}
}

// writeTestWAV writes a mono 48kHz f32 WAV of the given length where sample
// i has the value float32(i)/scale, so positions are recognizable.
func writeTestWAV(t *testing.T, path string, frames int) {
	t.Helper()
	enc, err := NewEncoderFile(path, EncoderConfigInit(EncodingFormatWAV, FormatF32, 1, 48000))
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]float32, frames)
	for i := range buf {
		buf[i] = float32(i%1000) / 1000
	}
	if _, err := enc.WritePCMFrames(f32ToBytes(buf), uint64(frames)); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncSourceStreaming(t *testing.T) {
	// A streamed (async) sound must produce the same audio as a fully
	// decoded one.
	path := t.TempDir() + "/stream.wav"
	const frames = 50000 // Larger than the async ring so refilling is exercised.
	writeTestWAV(t, path, frames)

	// A fresh engine per sound isolates the runs from each other.
	readAll := func(flags SoundFlags) []float32 {
		cfg := EngineConfigInit()
		cfg.NoDevice = true
		cfg.Channels = 1
		cfg.SampleRate = 48000
		e, err := NewEngine(&cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer e.Uninit()

		s, err := e.NewSoundFromFile(path, flags, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Uninit()

		s.Start()
		var out []float32
		buf := make([]float32, 480)
		// Streamed sounds may insert silence gaps when the feeder stalls
		// (the test drains faster than real time), so the iteration count
		// is unbounded; drive by AtEnd with a wall-clock safety deadline.
		deadline := time.Now().Add(10 * time.Second)
		for !s.AtEnd() && time.Now().Before(deadline) {
			e.ReadPCMFrames(buf, 480)
			out = append(out, buf...)
		}
		// Flush the tail still sitting in the graph's chunk cache.
		for i := 0; i < 3; i++ {
			e.ReadPCMFrames(buf, 480)
			out = append(out, buf...)
		}
		s.Stop()
		return out
	}

	sOut := readAll(0)
	dOut := readAll(SoundFlagDecode)

	if len(sOut) < frames || len(dOut) < frames {
		t.Fatalf("short outputs: streamed=%d decoded=%d", len(sOut), len(dOut))
	}
	// The test consumes the graph as fast as possible (no real-time pacing),
	// so the feeder may stall and the streamed output may contain inserted
	// silence gaps - by design, a stall produces silence rather than
	// blocking the callback. Verify the streamed output equals the decoded
	// output with silence gaps removed: walk both, absorbing zero-runs on
	// the streamed side.
	i := 0 // streamed
	j := 0 // decoded
	for j < frames {
		if i >= len(sOut) {
			t.Fatalf("streamed output exhausted at decoded sample %d", j)
		}
		if sOut[i] == dOut[j] {
			i++
			j++
			continue
		}
		if sOut[i] == 0 {
			i++ // Inserted stall gap.
			continue
		}
		t.Fatalf("decoded sample %d (%g) not found in streamed output (got %g at %d)",
			j, dOut[j], sOut[i], i)
	}
}

func TestAsyncSourceSeekAndLoop(t *testing.T) {
	path := t.TempDir() + "/seekloop.wav"
	const frames = 10000
	writeTestWAV(t, path, frames)

	decCfg := DecoderConfigInit(FormatF32, 0, 48000)
	dec, err := NewDecoderFile(path, &decCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()

	a, err := newAsyncSource(dec)
	if err != nil {
		t.Fatal(err)
	}
	defer a.stop()

	// Sequential read of the whole stream.
	got := make([]float32, 0, frames)
	buf := make([]float32, 512)
	deadline := 0
	for len(got) < frames && deadline < 100000 {
		read, rerr := a.Read(f32ToBytes(buf), 512)
		got = append(got, buf[:read]...)
		if rerr == ErrAtEnd {
			break
		}
		deadline++
	}
	if len(got) != frames {
		t.Fatalf("read %d frames, want %d", len(got), frames)
	}
	for i := 0; i < frames; i++ {
		want := float32(i%1000) / 1000
		if got[i] != want {
			t.Fatalf("sample %d = %g, want %g", i, got[i], want)
		}
	}

	// Seek back to a known position and verify data resumes there.
	if err := a.Seek(5000); err != nil {
		t.Fatal(err)
	}
	var first []float32
	for deadline := 0; deadline < 100000 && len(first) < 100; deadline++ {
		read, _ := a.Read(f32ToBytes(buf[:100]), 100)
		first = append(first, buf[:read]...)
	}
	if len(first) < 100 {
		t.Fatal("no data after seek")
	}
	for i := 0; i < 100; i++ {
		want := float32((5000+i)%1000) / 1000
		if first[i] != want {
			t.Fatalf("post-seek sample %d = %g, want %g", i, first[i], want)
		}
	}

	// Looping: read past the end and verify it wraps to the beginning.
	if err := a.SetLooping(true); err != nil {
		t.Fatal(err)
	}
	if err := a.Seek(frames - 50); err != nil {
		t.Fatal(err)
	}
	var wrap []float32
	for deadline := 0; deadline < 100000 && len(wrap) < 150; deadline++ {
		read, rerr := a.Read(f32ToBytes(buf[:150]), 150)
		wrap = append(wrap, buf[:read]...)
		if rerr == ErrAtEnd {
			t.Fatal("looping source reported ErrAtEnd")
		}
	}
	if len(wrap) < 150 {
		t.Fatal("looping source stalled")
	}
	for i := 0; i < 150; i++ {
		srcPos := (frames - 50 + i) % frames
		want := float32(srcPos%1000) / 1000
		if wrap[i] != want {
			t.Fatalf("loop sample %d (src %d) = %g, want %g", i, srcPos, wrap[i], want)
		}
	}
}

func TestNullDevice(t *testing.T) {
	ctx, err := InitContext([]Backend{BackendNull}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ctx.Uninit()

	if ctx.Backend() != BackendNull {
		t.Fatalf("backend = %v", ctx.Backend())
	}

	devices, err := ctx.Devices(DeviceTypePlayback)
	if err != nil || len(devices) != 1 {
		t.Fatalf("devices: %v %v", devices, err)
	}

	done := make(chan struct{}, 64)
	cfg := DeviceConfigInit(DeviceTypeDuplex)
	cfg.SampleRate = 48000
	cfg.PeriodSizeInMilliseconds = 5
	dev, err := InitDevice(ctx, cfg, DeviceCallbacks{
		Data: func(_ *Device, out, in []byte, frames uint32) {
			if out != nil && in != nil {
				copy(out, in)
			}
			select {
			case done <- struct{}{}:
			default:
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Uninit()

	if err := dev.Start(); err != nil {
		t.Fatal(err)
	}
	<-done // At least one callback fired.
	if dev.State() != DeviceStateStarted {
		t.Errorf("state = %v", dev.State())
	}
	if err := dev.Stop(); err != nil {
		t.Fatal(err)
	}
	if dev.State() != DeviceStateStopped {
		t.Errorf("state after stop = %v", dev.State())
	}
}

func TestEffects(t *testing.T) {
	// Panner.
	p, err := NewPanner(PannerConfigInit(FormatF32, 2))
	if err != nil {
		t.Fatal(err)
	}
	p.SetPan(1) // Full right in balance mode: left silent.
	src := []float32{1, 1}
	dst := make([]float32, 2)
	p.ProcessF32(dst, src, 1)
	if dst[0] != 0 || dst[1] != 1 {
		t.Errorf("pan right: %v", dst)
	}

	// Fader.
	f, err := NewFader(FaderConfigInit(FormatF32, 1, 48000))
	if err != nil {
		t.Fatal(err)
	}
	f.SetFade(0, 1, 100)
	in := make([]float32, 200)
	for i := range in {
		in[i] = 1
	}
	out := make([]float32, 200)
	f.ProcessF32(out, in, 200)
	if out[0] != 0 {
		t.Errorf("fade start = %f", out[0])
	}
	if out[150] != 1 {
		t.Errorf("fade end = %f", out[150])
	}
	if out[50] < 0.4 || out[50] > 0.6 {
		t.Errorf("fade middle = %f", out[50])
	}

	// Gainer.
	g, err := NewGainer(GainerConfigInit(1, 10))
	if err != nil {
		t.Fatal(err)
	}
	g.SetGain(0)
	gout := make([]float32, 20)
	g.Process(gout, in[:20], 20)
	if gout[0] == 0 {
		t.Error("gain should ramp, not jump")
	}
	if gout[15] != 0 {
		t.Errorf("gain should converge to 0, got %f", gout[15])
	}

	// Delay.
	d, err := NewDelay(DelayConfigInit(1, 48000, 10, 0.5))
	if err != nil {
		t.Fatal(err)
	}
	imp := make([]float32, 30)
	imp[0] = 1
	dout := make([]float32, 30)
	d.Process(dout, imp, 30)
	if dout[10] == 0 {
		t.Error("delay produced no echo at the delay length")
	}
}

func TestSpatializerDistanceAttenuation(t *testing.T) {
	l, err := NewSpatializerListener(SpatializerListenerConfigInit(2))
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSpatializer(SpatializerConfigInit(1, 2))
	if err != nil {
		t.Fatal(err)
	}

	src := make([]float32, 100)
	for i := range src {
		src[i] = 1
	}
	near := make([]float32, 200)
	far := make([]float32, 200)

	s.SetPosition(0, 0, -1)
	if err := s.Process(l, near, src, 100); err != nil {
		t.Fatal(err)
	}

	s2, _ := NewSpatializer(SpatializerConfigInit(1, 2))
	s2.SetPosition(0, 0, -100)
	if err := s2.Process(l, far, src, 100); err != nil {
		t.Fatal(err)
	}

	nearMax, farMax := maxAbsF32(near), maxAbsF32(far)
	if nearMax <= farMax {
		t.Errorf("distance attenuation failed: near=%f far=%f", nearMax, farMax)
	}
	if farMax == 0 {
		t.Error("far sound fully silent")
	}
}

func TestSpatializerProcessNoAlloc(t *testing.T) {
	l, err := NewSpatializerListener(SpatializerListenerConfigInit(2))
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSpatializer(SpatializerConfigInit(1, 2))
	if err != nil {
		t.Fatal(err)
	}
	s.SetPosition(1, 0, -2)

	src := make([]float32, 480)
	dst := make([]float32, 480*2)
	// Warm up (first call may snap gains).
	if err := s.Process(l, dst, src, 480); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(50, func() {
		if err := s.Process(l, dst, src, 480); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Errorf("Spatializer.Process allocates %.1f times per call; want 0", allocs)
	}
}

func maxAbsF32(s []float32) float64 {
	var m float64
	for _, v := range s {
		if a := math.Abs(float64(v)); a > m {
			m = a
		}
	}
	return m
}

func TestVolumeHelpers(t *testing.T) {
	if math.Abs(float64(VolumeLinearToDB(1))) > 1e-5 {
		t.Error("0dB != linear 1")
	}
	if math.Abs(float64(VolumeDBToLinear(-6))-0.5012) > 0.001 {
		t.Errorf("-6dB = %f, want ~0.5012", VolumeDBToLinear(-6))
	}
	s := []float32{2, -3, 0.5}
	ClipSamplesF32(s, s, 3)
	if s[0] != 1 || s[1] != -1 || s[2] != 0.5 {
		t.Errorf("clip: %v", s)
	}
}

// writeSeekBuffer is an in-memory io.WriteSeeker for encoder tests.
type writeSeekBuffer struct {
	buf []byte
	pos int
}

func (w *writeSeekBuffer) Write(p []byte) (int, error) {
	need := w.pos + len(p)
	if need > len(w.buf) {
		w.buf = append(w.buf, make([]byte, need-len(w.buf))...)
	}
	copy(w.buf[w.pos:], p)
	w.pos = need
	return len(p), nil
}

func (w *writeSeekBuffer) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case 0:
		w.pos = int(offset)
	case 1:
		w.pos += int(offset)
	case 2:
		w.pos = len(w.buf) + int(offset)
	}
	return int64(w.pos), nil
}
