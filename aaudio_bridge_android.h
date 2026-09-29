// Ring buffer and AAudio callbacks for the Android backend.
//
// The AAudio data callback runs on a dedicated high priority thread owned by
// the platform. It only ever memcpys between that thread's buffer and this
// SPSC ring, then signals an eventfd; it never calls into Go. Go feeds or
// drains the ring from an ordinary goroutine, so no cgo transition and no Go
// scheduler work ever happens on the realtime thread. This mirrors the
// CoreAudio backend's asm-callback design.

#ifndef GOMINIAUDIO_AAUDIO_BRIDGE_H
#define GOMINIAUDIO_AAUDIO_BRIDGE_H

#include <stdint.h>
#include <aaudio/AAudio.h>

// gma_ring is a single-producer/single-consumer byte ring.
//
// For playback Go is the producer and the callback the consumer; for capture
// the roles are reversed. read/write are free-running counters: their
// difference is the fill level, which stays correct across unsigned wrap, so
// they are never reset. capacity is a power of two and mask is capacity-1.
typedef struct gma_ring {
    uint8_t *buf;
    int32_t  capacity;
    int32_t  mask;
    int32_t  frame_size;
    int      evfd;      // eventfd signalled after every callback
    uint32_t read;      // atomic
    uint32_t write;     // atomic
    int32_t  xruns;     // atomic: callbacks that under/overran
    int32_t  error;     // atomic: last aaudio_result_t from the error callback
} gma_ring;

// Callbacks registered with AAudioStreamBuilder_setDataCallback.
aaudio_data_callback_result_t gma_playback_callback(AAudioStream *stream,
                                                    void *userData,
                                                    void *audioData,
                                                    int32_t numFrames);

aaudio_data_callback_result_t gma_capture_callback(AAudioStream *stream,
                                                   void *userData,
                                                   void *audioData,
                                                   int32_t numFrames);

// Registered with AAudioStreamBuilder_setErrorCallback.
void gma_error_callback(AAudioStream *stream, void *userData, aaudio_result_t error);

// gma_set_stream_callbacks installs the data and error callbacks on builder.
// Registration happens here rather than in Go because cgo cannot take the
// address of a C function. capture selects the input callback.
void gma_set_stream_callbacks(AAudioStreamBuilder *builder, gma_ring *ring, int capture);

// Ring accessors used from Go. All are safe to call from the non-callback
// side only.
int32_t gma_ring_writable(gma_ring *r);
int32_t gma_ring_readable(gma_ring *r);
void    gma_ring_commit_write(gma_ring *r, int32_t nbytes);
void    gma_ring_commit_read(gma_ring *r, int32_t nbytes);
int32_t gma_ring_write(gma_ring *r, const uint8_t *src, int32_t nbytes);
int32_t gma_ring_read(gma_ring *r, uint8_t *dst, int32_t nbytes);
int32_t gma_ring_xruns(gma_ring *r);
int32_t gma_ring_error(gma_ring *r);

#endif // GOMINIAUDIO_AAUDIO_BRIDGE_H
