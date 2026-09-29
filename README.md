# gominiaudio

A native Go implementation of [miniaudio](https://github.com/mackron/miniaudio),
with an API shaped like [malgo](https://github.com/gen2brain/malgo). Every
desktop backend is **cgo-free**: Windows, macOS and Linux are all driven
directly, in Go (plus a small amount of assembly on macOS).

Android is the one exception. Go has no libc-call bridge on Linux/Android
(`syscall/linkname_libc.go` is `aix || darwin || openbsd || solaris`), so the
`cgo_import_dynamic` trick used on macOS cannot work there and reaching
AAudio requires cgo. That backend is deliberately the thinnest possible
shim, and it is the only part of the library that needs a C toolchain.

**Dependencies:** the Go standard library and `golang.org/x/sys` only. No
third-party packages on any platform.

## Backends

| OS      | Backend    | Mechanism                                                                 |
|---------|------------|---------------------------------------------------------------------------|
| Windows | WASAPI     | COM vtable calls via `syscall`. Shared mode (IAudioClient3 small-period negotiation) and **exclusive mode** (native format probing, event-driven, aligned-buffer re-initialization). MMCSS "Pro Audio" thread priority. |
| Linux   | PipeWire   | The PipeWire *native protocol* spoken directly over the daemon's Unix socket: SPA POD serialization, memfd-backed buffers, eventfd scheduling. No libpipewire. |
| Linux   | PulseAudio | The PulseAudio *native protocol* spoken directly over the daemon's Unix socket: tagstruct serialization, cookie/`SCM_CREDENTIALS` auth, credit-based playback flow control. No libpulse. Also drives PipeWire's `pulse-server`. |
| macOS   | CoreAudio  | `//go:cgo_import_dynamic` symbol binding (the `x/sys/unix` technique) for HAL/AudioUnit calls; the real-time render callbacks are hand-written assembly that copy through a lock-free ring and signal a mach semaphore, so native code never calls into Go |
| Android | AAudio     | **The only cgo backend.** A thin binding to `libaaudio` (API 26+). The realtime data callback is pure C: it only memcpys between AAudio's buffer and an SPSC ring and signals an eventfd, so the platform's realtime thread never enters the Go runtime — the same principle as the CoreAudio asm callbacks. |
| all     | Null       | Timer-driven fake device for testing                                       |

Status:

- **WASAPI — verified on hardware.** Enumeration, playback, capture, duplex,
  loopback and exclusive mode.
- **PulseAudio — verified against a real daemon.** Connect, authenticate,
  enumerate sinks and sources, and run both playback and capture streams,
  tested against a live PulseAudio server. The wire format (command
  ordinals, tag encodings, sample formats, channel positions) is pinned by
  unit tests; the integration tests in `pa_integration_linux_test.go` skip
  automatically when no daemon is reachable.
- **PipeWire — experimental.** Compiles and vets cleanly but has never been
  run against a real daemon; it implements the client-node scheduling
  protocol from scratch, and the `pw_node_activation` struct offsets in
  particular were written from the PipeWire sources and need validating. On
  a PipeWire system the PulseAudio backend is the better-tested path today,
  via `pulse-server`.
- **CoreAudio — experimental.** Compiles and vets cleanly (darwin/amd64 and
  darwin/arm64); the hand-written asm callbacks need a real Mac to confirm
  ABI and struct offsets.
- **AAudio — experimental, never run on a device.** The cgo bindings
  compile and vet, the C ring buffer is unit tested (including wrap-around
  and under/overrun), and the full Go-feeder → ring → data-callback path is
  exercised against a stubbed AAudio, but no real Android hardware has run
  it. Device *enumeration* is also inherently limited: AAudio has no
  enumeration API, so the backend reports a single default playback and
  capture device and lets the platform route them, as miniaudio does.

Backend priority order is PipeWire → PulseAudio → Null on Linux, and
AAudio → Null on Android.

## Latency

The package aims for the lowest latency each OS allows:

- No allocations and no locks on the audio path (all buffers preallocated;
  lock-free SPSC ring buffers for duplex).
- Event-driven WASAPI with `IAudioClient3` small-period negotiation. On the
  low-latency profile with no explicit period requested, the backend asks
  the engine for its *minimum* shared-mode period rather than a generic
  10 ms default (measured: 480 → 96 frames / 2 ms on Surface hardware).
- Real-time thread priority on every backend's audio thread
  (`AvSetMmThreadCharacteristics` + `AvSetMmThreadPriority` critical on
  Windows, `SCHED_RR` on Linux, CoreAudio's own RT thread).
- Bounded buffering: the CoreAudio feeder queues at most two periods; the
  WASAPI shared-mode prefill is two periods; the duplex capture→playback
  bridge pre-seek is configurable (`DuplexPreSeekSizeInFrames`).
- No file I/O on the audio thread — streamed sounds decode on a background
  goroutine through a ring buffer, so a disk stall produces momentary
  silence rather than a blocked callback.
- Lock-free node-graph control plane — state, volume and topology changes
  never block the audio thread, avoiding priority inversion at small
  periods.

Measured with `examples/latency` through a VB-Audio virtual cable loopback
(48 kHz, Windows 11, shared mode), against the original C miniaudio running
the identical measurement. The virtual cable's internal buffer (~80 ms on
this route) dominates the *round-trip* figure, so the more telling number
is the negotiated device period, which is cable-independent:

|                                   | before | after |
|-----------------------------------|--------|-------|
| shared-mode period (low latency)  | 480 frames (10 ms) | 96 frames (2 ms) |
| data callbacks in 400 ms          | 44     | 203   |

The 480→96 frame drop came from fixing the `IAudioClient3` vtable slots
(the small-period path had been silently failing and falling back to the
10 ms classic path). Exclusive mode is unaffected by the engine and reaches
the driver's own minimum period directly.

## DSP performance optimisations

The DSP path contains several latency and throughput improvements:

- **Pre-allocated scratch buffers** — `DataConverter` and engine nodes
  pre-allocate `bufIn`/`bufMid`/`bufOut`/`srcScratch`/`midScratch` to
  cover typical period sizes (up to ~85 ms at 48 kHz), eliminating GC
  pressure during audio-thread warmup.
- **float32 Biquad state, register-resident inner loop** — coefficients
  and state use `float32` throughout (matching miniaudio's F32 path); the
  mono and stereo Biquad loops keep the filter state in locals so the
  frame loop runs register-resident with no per-sample state slice
  traffic. IIR filters are recursive in time and do not vectorise across
  frames, so this scalar specialisation — not SIMD — is the real
  stereo-filter win.
- **Block-filtered resampler** — the linear resampler's anti-aliasing LPF
  is applied to a whole block before/after interpolation instead of one
  frame at a time, removing tens of thousands of virtual-call chains per
  second per stream. Output is bit-identical regardless of chunk size
  (`TestLinearResamplerChunkInvariance`).
- **Specialised format conversions** — f32↔s16/s24/s32 have dedicated
  no-dither loops (float32 scale, no float64 widening). s24 matters on the
  device hot path: exclusive-mode WASAPI commonly negotiates 24-bit
  hardware formats.
- **Allocation-free spatialiser** — speaker geometry is precomputed at
  init; `Spatializer.Process` takes a single state snapshot under one
  lock and hands new gains to the gainer only when they change (so a
  stationary sound stops ramping). `TestSpatializerProcessNoAlloc` guards
  the zero-allocation property.
- **Lock-free node graph** — node state, volume, clocks and the graph
  time are atomics; topology changes are enqueued and applied by the
  reading thread at chunk boundaries. No control-plane call takes a lock
  the audio thread needs, so a preempted control goroutine cannot stall
  processing.

### SIMD acceleration (`GOEXPERIMENT=simd`, requires Go 1.27+)

When built with `GOEXPERIMENT=simd`, the package activates vector paths for
the hottest DSP operations using the standard library's portable
[`simd`](https://pkg.go.dev/simd) package. Unlike the older
`simd/archsimd`-only build, these paths are not amd64-specific: the same
source compiles to AVX2 on amd64, NEON on arm64, and wasm SIMD128 on wasm,
with a pure-Go emulated fallback on anything else. Hardware detection is
done at package `init` via `simd.Emulated()`; the scalar fallbacks remain
active on that emulated path, since a hand-written scalar loop beats the
generic per-element emulation.

One kernel — the no-dither f32→s16 conversion — needs a saturating pack and
a cross-lane permute that the portable package doesn't expose, so it stays
on the lower-level, amd64-only `simd/archsimd` package, gated by
`archsimd.X86.AVX2()`.

| Operation | File | Backend | Width | Speedup |
|---|---|---|---|---|
| `ClipSamplesF32` | `volume_simd.go` | simd (Max/Min) | native/iter | ~8× on AVX2 |
| `CopyAndApplyVolumeFactorF32` | `volume_simd.go` | simd (Mul) | native/iter | ~8× on AVX2 |
| `CopyAndApplyVolumeAndClipSamplesF32` | `volume_simd.go` | simd (Mul+Max+Min) | native/iter | ~8× on AVX2 |
| `MixPCMFramesF32` (dst+=src\*vol) | `volume_simd.go` | simd (MulAdd) | native/iter | ~8× on AVX2 |
| Channel weight mixing | `channel_converter_simd.go` | simd (MulAdd) | native out-ch/iter | 6–8× on AVX2 |
| Biquad filter (ch≥native width) | `filters_simd.go` | simd (MulAdd) | native ch/iter | ~4× on AVX2 |
| Linear resampler interpolation (ch≥native width) | `resampler_simd.go` | simd (MulAdd) | native ch/iter | ~4× on AVX2 |
| f32 → s16 (no dither) | `format_conversion_simd_amd64.go` | archsimd, AVX2 VCVTTPS2DQ+VPACKSSDW+VPERMQ | 8 samples/iter | ~4× |
| s16 → f32 | `format_conversion_simd_amd64.go` | archsimd, AVX2 VPMOVSXWD+VCVTDQ2PS | 8 samples/iter | ~4× |

"native" width is whatever `simd.Float32s.Len()` reports for the build's
target hardware (e.g. 8 on AVX2, 4 on AVX-only or NEON) — it's a runtime
value, not a compile-time constant, so the Biquad and per-frame resampler
SIMD paths compare the channel count against it directly rather than a
hardcoded 8. Below that width, the register-resident scalar specialisations
are faster, since these operations do not vectorise across frames. The
f32→s16 kernel reorders the `VPACKSSDW` output with `VPERMQ`, because that
instruction packs each 128-bit lane independently.

To build with SIMD support:

```sh
GOEXPERIMENT=simd go build ./...
GOEXPERIMENT=simd go test ./...
```

Both `simd` and `simd/archsimd` are experimental standard library packages
guarded by `GOEXPERIMENT=simd`, and are not subject to the Go 1
compatibility promise. `simd` is new in Go 1.27; `simd/archsimd` shipped
experimentally in Go 1.26 and had its amd64 API revised in Go 1.27 (its
full-vector `LoadX`/`.Store` now take slices directly — the old
`LoadXSlice`/`.StoreSlice` names are gone — and array-pointer loads/stores
moved to `LoadXArray`/`.StoreArray`). When `GOEXPERIMENT` is unset the
package compiles normally and runs with scalar implementations on all
platforms.

## API overview

The device API mirrors malgo; everything else mirrors miniaudio's C API
(`ma_foo_bar` → `FooBar`).

### Low-level: Context + Device

```go
ctx, err := ma.InitContext(nil, nil) // Platform default backend order.
defer ctx.Uninit()

devices, _ := ctx.Devices(ma.DeviceTypePlayback)

cfg := ma.DeviceConfigInit(ma.DeviceTypePlayback)
cfg.Playback.Format = ma.FormatF32
cfg.Playback.Channels = 2
cfg.SampleRate = 48000

dev, err := ma.InitDevice(ctx, cfg, ma.DeviceCallbacks{
    Data: func(dev *ma.Device, out, in []byte, frames uint32) {
        // Fill out with frames of interleaved f32 stereo.
    },
})
defer dev.Uninit()
dev.Start()
```

Device types: `Playback`, `Capture`, `Duplex`, `Loopback` (WASAPI).
The portable layer handles format/channel/rate conversion between your
requested client format and the device's native format, fixed-size
callbacks, and duplex bridging — the same guarantees miniaudio makes.

### High-level: Engine + Sounds

```go
engine, err := ma.NewEngine(nil) // Opens the default playback device.
defer engine.Uninit()

engine.Play("clip.wav", nil) // Fire and forget.

snd, _ := engine.NewSoundFromFile("music.wav", ma.SoundFlagStream, nil)
snd.SetVolume(0.8)
snd.SetPitch(1.2)
snd.SetPosition(3, 0, -1) // 3D spatialization.
snd.Start()
```

### The rest of the miniaudio surface

- **Data conversion**: `PCMConvert` (all format pairs, dithering),
  `ChannelConverter` (rectangular/simple/custom weights),
  `LinearResampler`/`Resampler` (custom algorithms pluggable),
  `DataConverter` (the full pipeline).
- **Filters**: `Biquad`, `LPF1/LPF2/LPF` (Nth order Butterworth), `HPF*`,
  `BPF*`, `Notch2`, `Peak2`, `Loshelf2`, `Hishelf2`.
- **Effects**: `Panner`, `Fader`, `Gainer`, `Delay`; full 3D `Spatializer`
  with attenuation models, cones and doppler.
- **Generators**: `Waveform` (sine/square/triangle/sawtooth), `Noise`
  (white/pink/brownian).
- **Data sources**: the `DataSource` interface with looping, ranges, loop
  points and chaining (`DataSourceController`); `AudioBuffer(Ref)`.
- **Decoding/encoding**: WAV built in (`Decoder`, `Encoder`); other formats
  plug in through the `DecodingBackend` interface, mirroring miniaudio's
  pluggable decoding backends.
- **Node graph**: `NodeGraph` with data source, splitter, filter and delay
  nodes; custom nodes via the `NodeProcessor` interface.
- **Utilities**: lock-free ring buffers (`RB`, `PCMRB`, `DuplexRB`), volume
  and clipping helpers, channel map helpers.

## Examples

- `examples/passthrough` — streams the default capture device to the
  default playback device (duplex).
- `examples/latency` — round-trip latency measurement (impulse through a
  loopback), plus `examples/latency/c/miniaudio_latency.c`, the identical
  measurement implemented with the original C miniaudio for comparison.
  Build instructions are at the top of the C file.

```sh
go run ./examples/passthrough
go run ./examples/latency -out "CABLE In" -in "CABLE Output"
```

## Building for Android

Android is the only target that needs a C toolchain, because the AAudio
backend is cgo. Point `CC` at the NDK's clang for the API level and ABI you
are targeting (26 or later — AAudio does not exist below that):

```sh
export ANDROID_NDK_HOME=/path/to/android-ndk
export TOOLCHAIN="$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/linux-x86_64/bin"

CGO_ENABLED=1 GOOS=android GOARCH=arm64   CC="$TOOLCHAIN/aarch64-linux-android26-clang"   go build ./...
```

`gomobile bind` works too and sets `CC` for you. Every other platform builds
with `CGO_ENABLED=0` and needs no C compiler at all.

Note that `GOOS=android` also satisfies the `linux` build tag, so the
PipeWire and PulseAudio backends carry an explicit `!android` constraint and
Android gets its own dispatch in `backend_dispatch_android.go`.

## Testing

```sh
go test ./...
```

The test suite covers the DSP core (conversion round-trips, resampler
ratios, filter stability), the WAV codec, data sources, the node graph, the
engine, and the device lifecycle against the Null backend.

On Linux it additionally runs the PulseAudio protocol tests, which pin the
wire format, plus integration tests against a real daemon. Those skip
automatically when no PulseAudio server is reachable, so they are safe to
run anywhere. One of them plays an audible tone and is opt-in:

```sh
GOMINIAUDIO_AUDIO_TEST=1 go test -run TestPulsePlaybackTone -v
```

## Differences from miniaudio

- Success/failure is reported with Go `error` values; the `Result` codes
  from `ma_result` are preserved and implement `error`.
- Only WAV decoding is built in (miniaudio bundles dr_flac/dr_mp3/
  stb_vorbis); FLAC/MP3/Vorbis decoders can be plugged in via
  `DecoderConfig.CustomBackends`.
- The AAudio/OpenSL/ALSA/PulseAudio/JACK/sndio family of backends is
  intentionally out of scope: this library targets each OS's current audio
  subsystem only (WASAPI shared+exclusive, PipeWire, CoreAudio).
  Exclusive-mode loopback capture is rejected (a WASAPI limitation).
- The resource manager's async loading is synchronous (`SoundFlagAsync` is
  accepted but loads eagerly). Streamed sounds (`SoundFlagStream`) do decode
  on a background goroutine, but do not support loop points or ranges — use
  `SoundFlagDecode` when those are needed.
