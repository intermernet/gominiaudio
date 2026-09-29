// AAudio realtime callbacks. See aaudio_bridge_android.h for the design.
//
// Nothing here allocates, locks, logs or calls into Go: the only operations
// on the realtime thread are two atomic loads, a pair of memcpys, one atomic
// store and an 8 byte write() to an eventfd.

#include "aaudio_bridge_android.h"

#include <stdatomic.h>
#include <string.h>
#include <unistd.h>

// Treat the counters as C11 atomics without changing the struct layout Go
// sees. uint32_t and _Atomic uint32_t have the same size and alignment on
// every Android ABI, and only these helpers touch the fields.
#define AT_U32(p) ((_Atomic uint32_t *)(p))
#define AT_I32(p) ((_Atomic int32_t *)(p))

// fill returns the number of readable bytes. The subtraction is wrap-safe.
static inline int32_t ring_fill(gma_ring *r) {
    uint32_t w = atomic_load_explicit(AT_U32(&r->write), memory_order_acquire);
    uint32_t rd = atomic_load_explicit(AT_U32(&r->read), memory_order_relaxed);
    return (int32_t)(w - rd);
}

// space returns the number of writable bytes.
static inline int32_t ring_space(gma_ring *r) {
    return r->capacity - ring_fill(r);
}

// copy_out moves n bytes out of the ring starting at the read cursor.
static inline void copy_out(gma_ring *r, uint8_t *dst, uint32_t rd, int32_t n) {
    int32_t off = (int32_t)(rd & (uint32_t)r->mask);
    int32_t first = r->capacity - off;
    if (first > n) {
        first = n;
    }
    memcpy(dst, r->buf + off, (size_t)first);
    if (n > first) {
        memcpy(dst + first, r->buf, (size_t)(n - first));
    }
}

// copy_in moves n bytes into the ring starting at the write cursor.
static inline void copy_in(gma_ring *r, const uint8_t *src, uint32_t wr, int32_t n) {
    int32_t off = (int32_t)(wr & (uint32_t)r->mask);
    int32_t first = r->capacity - off;
    if (first > n) {
        first = n;
    }
    memcpy(r->buf + off, src, (size_t)first);
    if (n > first) {
        memcpy(r->buf, src + first, (size_t)(n - first));
    }
}

// wake nudges the Go side. EAGAIN just means the counter is saturated and a
// wakeup is already pending, which is harmless.
static inline void wake(gma_ring *r) {
    if (r->evfd >= 0) {
        uint64_t one = 1;
        ssize_t ignored = write(r->evfd, &one, sizeof(one));
        (void)ignored;
    }
}

aaudio_data_callback_result_t gma_playback_callback(AAudioStream *stream,
                                                    void *userData,
                                                    void *audioData,
                                                    int32_t numFrames) {
    (void)stream;
    gma_ring *r = (gma_ring *)userData;
    uint8_t *out = (uint8_t *)audioData;
    int32_t want = numFrames * r->frame_size;

    int32_t avail = ring_fill(r);
    int32_t n = avail < want ? avail : want;

    if (n > 0) {
        uint32_t rd = atomic_load_explicit(AT_U32(&r->read), memory_order_relaxed);
        copy_out(r, out, rd, n);
        atomic_store_explicit(AT_U32(&r->read), rd + (uint32_t)n, memory_order_release);
    }
    if (n < want) {
        // Underrun: the feeder did not keep up. Output silence rather than
        // stale audio and record it for the Go side.
        memset(out + n, 0, (size_t)(want - n));
        atomic_fetch_add_explicit(AT_I32(&r->xruns), 1, memory_order_relaxed);
    }

    wake(r);
    return AAUDIO_CALLBACK_RESULT_CONTINUE;
}

aaudio_data_callback_result_t gma_capture_callback(AAudioStream *stream,
                                                   void *userData,
                                                   void *audioData,
                                                   int32_t numFrames) {
    (void)stream;
    gma_ring *r = (gma_ring *)userData;
    const uint8_t *in = (const uint8_t *)audioData;
    int32_t have = numFrames * r->frame_size;

    int32_t space = ring_space(r);
    int32_t n = space < have ? space : have;

    if (n > 0) {
        uint32_t wr = atomic_load_explicit(AT_U32(&r->write), memory_order_relaxed);
        copy_in(r, in, wr, n);
        atomic_store_explicit(AT_U32(&r->write), wr + (uint32_t)n, memory_order_release);
    }
    if (n < have) {
        // Overrun: the drainer did not keep up, so the tail is dropped.
        atomic_fetch_add_explicit(AT_I32(&r->xruns), 1, memory_order_relaxed);
    }

    wake(r);
    return AAUDIO_CALLBACK_RESULT_CONTINUE;
}

void gma_error_callback(AAudioStream *stream, void *userData, aaudio_result_t error) {
    (void)stream;
    gma_ring *r = (gma_ring *)userData;
    atomic_store_explicit(AT_I32(&r->error), (int32_t)error, memory_order_release);
    wake(r);
}

void gma_set_stream_callbacks(AAudioStreamBuilder *builder, gma_ring *ring, int capture) {
    AAudioStreamBuilder_setDataCallback(builder,
        capture ? gma_capture_callback : gma_playback_callback, ring);
    AAudioStreamBuilder_setErrorCallback(builder, gma_error_callback, ring);
}

int32_t gma_ring_writable(gma_ring *r) {
    return ring_space(r);
}

int32_t gma_ring_readable(gma_ring *r) {
    return ring_fill(r);
}

// gma_ring_write copies from src into the ring, returning the bytes taken.
int32_t gma_ring_write(gma_ring *r, const uint8_t *src, int32_t nbytes) {
    int32_t space = ring_space(r);
    int32_t n = space < nbytes ? space : nbytes;
    if (n <= 0) {
        return 0;
    }
    uint32_t wr = atomic_load_explicit(AT_U32(&r->write), memory_order_relaxed);
    copy_in(r, src, wr, n);
    atomic_store_explicit(AT_U32(&r->write), wr + (uint32_t)n, memory_order_release);
    return n;
}

// gma_ring_read copies out of the ring into dst, returning the bytes moved.
int32_t gma_ring_read(gma_ring *r, uint8_t *dst, int32_t nbytes) {
    int32_t avail = ring_fill(r);
    int32_t n = avail < nbytes ? avail : nbytes;
    if (n <= 0) {
        return 0;
    }
    uint32_t rd = atomic_load_explicit(AT_U32(&r->read), memory_order_relaxed);
    copy_out(r, dst, rd, n);
    atomic_store_explicit(AT_U32(&r->read), rd + (uint32_t)n, memory_order_release);
    return n;
}

void gma_ring_commit_write(gma_ring *r, int32_t nbytes) {
    uint32_t wr = atomic_load_explicit(AT_U32(&r->write), memory_order_relaxed);
    atomic_store_explicit(AT_U32(&r->write), wr + (uint32_t)nbytes, memory_order_release);
}

void gma_ring_commit_read(gma_ring *r, int32_t nbytes) {
    uint32_t rd = atomic_load_explicit(AT_U32(&r->read), memory_order_relaxed);
    atomic_store_explicit(AT_U32(&r->read), rd + (uint32_t)nbytes, memory_order_release);
}

int32_t gma_ring_xruns(gma_ring *r) {
    return atomic_load_explicit(AT_I32(&r->xruns), memory_order_relaxed);
}

int32_t gma_ring_error(gma_ring *r) {
    return atomic_load_explicit(AT_I32(&r->error), memory_order_acquire);
}
