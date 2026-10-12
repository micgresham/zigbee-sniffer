// sdr_spectrum.c — see sdr_spectrum.h.
#include "sdr_spectrum.h"
#include <math.h>
#include <string.h>
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#define FFT_MAX     (1u << SDR_FFT_LOG2_MAX)
#define WIN_SHIFT   9           // (10-bit sample x Q15 window) >> 9 keeps ~15 bits
#define SAMPLE_FS   511         // full-scale magnitude of a signed 10-bit component

static int32_t  s_re[FFT_MAX], s_im[FFT_MAX];
static uint64_t s_acc[FFT_MAX];
static uint8_t  s_codes[FFT_MAX];
static int16_t  s_win[FFT_MAX];
static int16_t  s_cos[FFT_MAX / 2], s_sin[FFT_MAX / 2];
static unsigned s_tab_log2;
static float    s_ref_db;       // 10*log10(power of a full-scale tone in one bin)
static uint16_t s_sweep_id;

static void build_tables(unsigned log2n)
{
    if (s_tab_log2 == log2n) return;
    const unsigned n = 1u << log2n;
    const float two_pi = 6.28318530718f;
    int64_t sumw = 0;
    for (unsigned i = 0; i < n; i++) {
        float w = 0.5f - 0.5f * cosf(two_pi * (float)i / (float)n);   // Hann
        s_win[i] = (int16_t)lrintf(w * 32767.0f);
        sumw += s_win[i];
    }
    for (unsigned k = 0; k < n / 2; k++) {                             // e^{-j2πk/n}
        s_cos[k] = (int16_t)lrintf(cosf(two_pi * (float)k / (float)n) * 32767.0f);
        s_sin[k] = (int16_t)lrintf(-sinf(two_pi * (float)k / (float)n) * 32767.0f);
    }
    // A full-scale complex tone on a bin center sums coherently over the window.
    float fs_mag = (float)SAMPLE_FS * (float)sumw / (float)(1u << WIN_SHIFT);
    s_ref_db = 20.0f * log10f(fs_mag);
    s_tab_log2 = log2n;
}

// In-place radix-2 decimation-in-time FFT on Q-format integers. No per-stage
// scaling: windowed inputs are <= 2^15, so even 512 points grow to < 2^24 and
// twiddle products are formed in 64 bits.
static void fft(int32_t *re, int32_t *im, unsigned log2n)
{
    const unsigned n = 1u << log2n;
    for (unsigned i = 1, j = 0; i < n; i++) {       // bit-reversal permutation
        unsigned bit = n >> 1;
        for (; j & bit; bit >>= 1) j ^= bit;
        j ^= bit;
        if (i < j) {
            int32_t t = re[i]; re[i] = re[j]; re[j] = t;
            t = im[i]; im[i] = im[j]; im[j] = t;
        }
    }
    for (unsigned len = 2, step = n / 2; len <= n; len <<= 1, step >>= 1) {
        const unsigned half = len >> 1;
        for (unsigned base = 0; base < n; base += len) {
            for (unsigned k = 0; k < half; k++) {
                const int32_t wr = s_cos[k * step], wi = s_sin[k * step];
                int32_t *ar = &re[base + k], *ai = &im[base + k];
                int32_t *br = &re[base + k + half], *bi = &im[base + k + half];
                int32_t tr = (int32_t)(((int64_t)*br * wr - (int64_t)*bi * wi) >> 15);
                int32_t ti = (int32_t)(((int64_t)*br * wi + (int64_t)*bi * wr) >> 15);
                *br = *ar - tr; *bi = *ai - ti;
                *ar += tr;      *ai += ti;
            }
        }
    }
}

void sdr_spectrum_default_config(sdr_sweep_config_t *cfg)
{
    memset(cfg, 0, sizeof(*cfg));
    static const uint16_t los[] = { 2412, 2427, 2442, 2457, 2472 };
    cfg->n_lo = sizeof(los) / sizeof(los[0]);
    memcpy(cfg->lo_mhz, los, sizeof(los));
    cfg->fft_log2 = SDR_FFT_LOG2_DEF;
    cfg->n_avg = 0;
    cfg->detector = SDR_DET_MEAN;
}

bool sdr_spectrum_config_valid(const sdr_sweep_config_t *cfg)
{
    if (cfg->n_lo < 1 || cfg->n_lo > SDR_MAX_LO) return false;
    if (cfg->fft_log2 < SDR_FFT_LOG2_MIN || cfg->fft_log2 > SDR_FFT_LOG2_MAX) return false;
    if (cfg->detector > SDR_DET_PEAK) return false;
    for (unsigned i = 0; i < cfg->n_lo; i++) {
        if (cfg->lo_mhz[i] < SDR_TUNE_MIN_MHZ || cfg->lo_mhz[i] > SDR_TUNE_MAX_MHZ) return false;
    }
    return true;
}

// Reduce `n_ffts` consecutive blocks of `samples` into s_codes (FFT-shifted:
// index 0 = LO - fs/2). Fills the gain/clip fields of `seg`.
static void reduce(const uint32_t *samples, unsigned log2n, unsigned n_ffts, bool peak,
                   sdr_segment_t *seg)
{
    const unsigned n = 1u << log2n;
    unsigned gmin = 0xFF, gmax = 0;
    bool clipped = false;
    memset(s_acc, 0, n * sizeof(s_acc[0]));

    for (unsigned b = 0; b < n_ffts; b++) {
        const uint32_t *blk = samples + b * n;
        // Per-block mean removal knocks down the ADC/LO DC term, which would
        // otherwise dominate the center bins and leak through the window.
        int32_t si = 0, sq = 0;
        for (unsigned i = 0; i < n; i++) {
            uint32_t w = blk[i];
            int vi = sdr_sample_i(w), vq = sdr_sample_q(w);
            si += vi; sq += vq;
            unsigned g = sdr_sample_gain(w);
            if (g < gmin) gmin = g;
            if (g > gmax) gmax = g;
            if (vi >= SAMPLE_FS || vi <= -SAMPLE_FS || vq >= SAMPLE_FS || vq <= -SAMPLE_FS) clipped = true;
        }
        const int32_t mi = si / (int32_t)n, mq = sq / (int32_t)n;
        for (unsigned i = 0; i < n; i++) {
            uint32_t w = blk[i];
            s_re[i] = ((sdr_sample_i(w) - mi) * (int32_t)s_win[i]) >> WIN_SHIFT;
            s_im[i] = ((sdr_sample_q(w) - mq) * (int32_t)s_win[i]) >> WIN_SHIFT;
        }
        fft(s_re, s_im, log2n);
        for (unsigned k = 0; k < n; k++) {
            uint64_t p = (uint64_t)((int64_t)s_re[k] * s_re[k]) + (uint64_t)((int64_t)s_im[k] * s_im[k]);
            if (peak) { if (p > s_acc[k]) s_acc[k] = p; }
            else s_acc[k] += p;
        }
    }

    const float div = peak ? 1.0f : (float)n_ffts;
    for (unsigned m = 0; m < n; m++) {
        uint64_t p = s_acc[(m + n / 2) & (n - 1)];    // fftshift
        int code = 0;
        if (p) {
            float dbfs = 10.0f * log10f((float)p / div) - s_ref_db;
            code = (int)lrintf((dbfs - (float)SDR_CODE_FLOOR_DBFS) * SDR_CODES_PER_DB);
            if (code < 0) code = 0;
            if (code > 255) code = 255;
        }
        s_codes[m] = (uint8_t)code;
    }
    seg->gain_min = (uint8_t)gmin;
    seg->gain_max = (uint8_t)gmax;
    if (clipped) seg->flags |= SDR_SEG_CLIPPED;
}

uint16_t sdr_spectrum_sweep(const sdr_sweep_config_t *cfg, sdr_segment_cb_t cb, void *ctx)
{
    const uint16_t id = ++s_sweep_id;
    if (!sdr_spectrum_config_valid(cfg)) return id;
    const unsigned log2n = cfg->fft_log2, n = 1u << log2n;
    const unsigned max_ffts = SDR_MAX_SAMPLES / n;
    unsigned n_ffts = cfg->n_avg ? cfg->n_avg : max_ffts;
    if (n_ffts > max_ffts) n_ffts = max_ffts;
    const bool peak = cfg->detector == SDR_DET_PEAK;
    build_tables(log2n);

    for (unsigned s = 0; s < cfg->n_lo; s++) {
        sdr_segment_t seg = {
            .sweep_id = id, .seg_idx = (uint8_t)s, .seg_count = cfg->n_lo,
            .lo_mhz = cfg->lo_mhz[s], .fft_log2 = (uint8_t)log2n,
            .sample_rate_hz = SDR_SAMPLE_RATE_HZ, .n_ffts = (uint8_t)n_ffts,
            .flags = peak ? SDR_SEG_PEAK : 0, .n_bins = (uint16_t)n, .codes = s_codes,
        };
        const uint32_t *samples;
        if (sdr_capture_tune(cfg->lo_mhz[s]) && sdr_capture_snapshot(n_ffts * n, &samples)) {
            reduce(samples, log2n, n_ffts, peak, &seg);
        } else {
            memset(s_codes, 0, n);
            seg.flags |= SDR_SEG_FAILED;
            seg.n_ffts = 0;
        }
        seg.timestamp_ms = (uint32_t)(esp_timer_get_time() / 1000);
        if (cb) cb(&seg, ctx);
        vTaskDelay(1);  // FFT work is ~10 ms/segment — let lower-priority tasks breathe
    }
    return id;
}
