/*
Round-trip latency measurement using the original C miniaudio, mirroring
the logic of main.go so the two implementations can be compared directly.

Build (after downloading miniaudio.h from https://github.com/mackron/miniaudio
into this directory):

  Windows (MSVC):  cl /O2 miniaudio_latency.c /Fe:miniaudio_latency.exe
  Windows (MinGW): gcc -O2 miniaudio_latency.c -o miniaudio_latency.exe -lole32 -lwinmm
  Linux:           gcc -O2 miniaudio_latency.c -o miniaudio_latency -lpthread -lm -ldl
  macOS:           clang -O2 miniaudio_latency.c -o miniaudio_latency -lpthread -lm \
                     -framework CoreFoundation -framework CoreAudio -framework AudioToolbox

Usage:

  miniaudio_latency [-out <substr>] [-in <substr>] [-rate <hz>] [-period <frames>] [-trials <n>]

Route the chosen playback device into the capture device with a loopback
(physical cable, virtual cable, or monitor source) before running.
*/

#define MINIAUDIO_IMPLEMENTATION
#include "miniaudio.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#define IMPULSE_GAP_FRAMES 24000
#define THRESHOLD 0.25f
#define MAX_TRIALS 256

static double now_ms(void)
{
#if defined(_WIN32)
    static LARGE_INTEGER freq;
    LARGE_INTEGER t;
    if (freq.QuadPart == 0) QueryPerformanceFrequency(&freq);
    QueryPerformanceCounter(&t);
    return (double)t.QuadPart * 1000.0 / (double)freq.QuadPart;
#else
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (double)ts.tv_sec * 1000.0 + (double)ts.tv_nsec / 1e6;
#endif
}

typedef struct {
    ma_int64 emitCountdown;
    double   emitTimeMS;
    volatile int armed;
    volatile int resultCount;
    double   results[MAX_TRIALS];
    int      trials;
} latency_state;

static void data_callback(ma_device* pDevice, void* pOutput, const void* pInput, ma_uint32 frameCount)
{
    latency_state* st = (latency_state*)pDevice->pUserData;
    float* out = (float*)pOutput;
    const float* in = (const float*)pInput;
    ma_uint32 i;

    /* Detect the impulse in the captured input. */
    if (st->armed && in != NULL) {
        for (i = 0; i < frameCount; i += 1) {
            float v = in[i * 2];
            if (v > THRESHOLD || v < -THRESHOLD) {
                if (st->armed) {
                    st->armed = 0;
                    if (st->resultCount < st->trials && st->resultCount < MAX_TRIALS) {
                        st->results[st->resultCount++] = now_ms() - st->emitTimeMS;
                    }
                }
                break;
            }
        }
    }

    /* Generate output: silence, with a periodic impulse. */
    if (out == NULL) return;
    memset(out, 0, (size_t)frameCount * 2 * sizeof(float));

    st->emitCountdown -= frameCount;
    if (st->emitCountdown <= 0 && !st->armed) {
        ma_uint32 n = frameCount < 16 ? frameCount : 16;
        for (i = 0; i < n; i += 1) {
            out[i * 2 + 0] = 0.9f;
            out[i * 2 + 1] = 0.9f;
        }
        st->emitTimeMS = now_ms();
        st->armed = 1;
        st->emitCountdown = IMPULSE_GAP_FRAMES;
    }
}

static int find_device(ma_context* ctx, ma_device_type type, const char* substr, ma_device_id* outID, char* outName, size_t outNameCap)
{
    ma_device_info* pPlayback;
    ma_uint32 playbackCount;
    ma_device_info* pCapture;
    ma_uint32 captureCount;
    ma_device_info* list;
    ma_uint32 count, i;

    if (substr == NULL || substr[0] == '\0') {
        snprintf(outName, outNameCap, "(default)");
        return 0; /* Use default. */
    }
    if (ma_context_get_devices(ctx, &pPlayback, &playbackCount, &pCapture, &captureCount) != MA_SUCCESS) {
        return -1;
    }
    list = (type == ma_device_type_playback) ? pPlayback : pCapture;
    count = (type == ma_device_type_playback) ? playbackCount : captureCount;
    for (i = 0; i < count; i += 1) {
        if (strstr(list[i].name, substr) != NULL) {
            *outID = list[i].id;
            snprintf(outName, outNameCap, "%s", list[i].name);
            return 1;
        }
    }
    fprintf(stderr, "no device matching \"%s\"\n", substr);
    return -1;
}

static int cmp_double(const void* a, const void* b)
{
    double da = *(const double*)a, db = *(const double*)b;
    return (da > db) - (da < db);
}

int main(int argc, char** argv)
{
    const char* outSub = "";
    const char* inSub = "";
    ma_uint32 rate = 48000;
    ma_uint32 period = 0;
    int trials = 10;
    int exclusive = 0;
    int i;

    for (i = 1; i < argc; i += 1) {
        if (strcmp(argv[i], "-exclusive") == 0) { exclusive = 1; continue; }
        if (i >= argc - 1) break;
        if (strcmp(argv[i], "-out") == 0) outSub = argv[++i];
        else if (strcmp(argv[i], "-in") == 0) inSub = argv[++i];
        else if (strcmp(argv[i], "-rate") == 0) rate = (ma_uint32)atoi(argv[++i]);
        else if (strcmp(argv[i], "-period") == 0) period = (ma_uint32)atoi(argv[++i]);
        else if (strcmp(argv[i], "-trials") == 0) trials = atoi(argv[++i]);
    }
    if (trials > MAX_TRIALS) trials = MAX_TRIALS;

    ma_context ctx;
    if (ma_context_init(NULL, 0, NULL, &ctx) != MA_SUCCESS) {
        fprintf(stderr, "context init failed\n");
        return 1;
    }
    printf("backend: %s\n", ma_get_backend_name(ctx.backend));

    ma_device_id outID, inID;
    char outLabel[256], inLabel[256];
    int haveOut = find_device(&ctx, ma_device_type_playback, outSub, &outID, outLabel, sizeof(outLabel));
    int haveIn = find_device(&ctx, ma_device_type_capture, inSub, &inID, inLabel, sizeof(inLabel));
    if (haveOut < 0 || haveIn < 0) return 1;
    printf("playback: %s\ncapture:  %s\n", outLabel, inLabel);

    latency_state st;
    memset(&st, 0, sizeof(st));
    st.emitCountdown = rate; /* 1s warmup. */
    st.trials = trials;

    ma_device_config cfg = ma_device_config_init(ma_device_type_duplex);
    cfg.sampleRate = rate;
    cfg.periodSizeInFrames = period;
    cfg.playback.format = ma_format_f32;
    cfg.playback.channels = 2;
    if (haveOut == 1) cfg.playback.pDeviceID = &outID;
    cfg.capture.format = ma_format_f32;
    cfg.capture.channels = 2;
    if (haveIn == 1) cfg.capture.pDeviceID = &inID;
    cfg.performanceProfile = ma_performance_profile_low_latency;
    if (exclusive) {
        cfg.playback.shareMode = ma_share_mode_exclusive;
        cfg.capture.shareMode = ma_share_mode_exclusive;
    }
    cfg.dataCallback = data_callback;
    cfg.pUserData = &st;

    ma_device dev;
    if (ma_device_init(&ctx, &cfg, &dev) != MA_SUCCESS) {
        fprintf(stderr, "device init failed\n");
        return 1;
    }
    printf("negotiated: playback period=%u frames, capture period=%u frames @ %uHz\n",
           dev.playback.internalPeriodSizeInFrames, dev.capture.internalPeriodSizeInFrames, dev.sampleRate);

    if (ma_device_start(&dev) != MA_SUCCESS) {
        fprintf(stderr, "device start failed\n");
        return 1;
    }

    double deadline = now_ms() + trials * 1000.0 + 10000.0;
    int reported = 0;
    while (st.resultCount < trials && now_ms() < deadline) {
        while (reported < st.resultCount) {
            printf("  impulse %2d: %7.2f ms\n", reported + 1, st.results[reported]);
            reported += 1;
        }
#if defined(_WIN32)
        Sleep(10);
#else
        struct timespec ts = {0, 10 * 1000 * 1000};
        nanosleep(&ts, NULL);
#endif
    }
    ma_device_stop(&dev);

    int n = st.resultCount;
    if (n == 0) {
        fprintf(stderr, "timed out waiting for impulses; is the loopback routed?\n");
        return 1;
    }
    qsort(st.results, (size_t)n, sizeof(double), cmp_double);
    double sum = 0;
    for (i = 0; i < n; i += 1) sum += st.results[i];
    printf("\nround-trip latency over %d impulses:\n", n);
    printf("  min:    %7.2f ms\n", st.results[0]);
    printf("  median: %7.2f ms\n", st.results[n / 2]);
    printf("  avg:    %7.2f ms\n", sum / n);
    printf("  max:    %7.2f ms\n", st.results[n - 1]);

    ma_device_uninit(&dev);
    ma_context_uninit(&ctx);
    return 0;
}
