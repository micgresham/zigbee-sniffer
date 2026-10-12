// sdr_spectrum.h — wideband power spectrum from sdr_capture snapshots.
//
// One "sweep" visits a list of LO frequencies. At each LO it takes a snapshot,
// splits it into back-to-back FFT blocks (DC-removed, Hann-windowed,
// fixed-point FFT — the C6 has no FPU), and reduces them to one power spectrum
// per LO with either a mean or a peak-hold detector. Each LO's result is a
// "segment" of 2^fft_log2 bins spanning SDR_SAMPLE_RATE_HZ centered on the LO;
// the host stitches segments into a full-band trace.
//
// Self-contained like sdr_capture (no zigbee-sniffer protocol dependencies).
#pragma once

#include <stdbool.h>
#include <stdint.h>
#include "sdr_capture.h"

#ifdef __cplusplus
extern "C" {
#endif

#define SDR_MAX_LO        8
#define SDR_FFT_LOG2_MIN  7     // 128 bins (625 kHz)
#define SDR_FFT_LOG2_MAX  9     // 512 bins (156 kHz)
#define SDR_FFT_LOG2_DEF  8     // 256 bins (312.5 kHz)

// Power codes: dBFS = code / SDR_CODES_PER_DB + SDR_CODE_FLOOR_DBFS. Code 0 is
// a floor (<= -128 dBFS), not proof of silence; 255 is -0.5 dBFS (0 dBFS = a
// full-scale complex tone centered in a bin).
#define SDR_CODES_PER_DB     2
#define SDR_CODE_FLOOR_DBFS  (-128)

enum {
    SDR_DET_MEAN = 0,
    SDR_DET_PEAK = 1,
};

// sdr_segment_t.flags
#define SDR_SEG_PEAK      0x01  // codes are peak-hold, not mean
#define SDR_SEG_FAILED    0x02  // tune/capture failed; codes are all zero
#define SDR_SEG_CLIPPED   0x04  // ADC hit full scale in this snapshot
#define SDR_SEG_DROPPED   0x08  // set by the caller: an earlier segment was lost in transport

typedef struct {
    uint8_t  n_lo;                   // 1..SDR_MAX_LO
    uint16_t lo_mhz[SDR_MAX_LO];
    uint8_t  fft_log2;               // SDR_FFT_LOG2_MIN..MAX
    uint8_t  n_avg;                  // FFT blocks per segment; 0 = as many as fit
    uint8_t  detector;               // SDR_DET_*
} sdr_sweep_config_t;

typedef struct {
    uint16_t sweep_id;
    uint8_t  seg_idx, seg_count;
    uint16_t lo_mhz;
    uint8_t  fft_log2;
    uint32_t sample_rate_hz;
    uint8_t  n_ffts;                 // blocks actually reduced into this segment
    uint8_t  flags;                  // SDR_SEG_*
    uint8_t  gain_min, gain_max;     // RX gain index range seen in the snapshot
    uint32_t timestamp_ms;
    uint16_t n_bins;
    const uint8_t *codes;            // n_bins power codes, lowest frequency first
} sdr_segment_t;

typedef void (*sdr_segment_cb_t)(const sdr_segment_t *seg, void *ctx);

// 2412..2472 MHz in 15 MHz steps: five Wi-Fi channel centers whose central
// +/-7.5 MHz tiles 2404.5..2479.5 MHz and whose +/-40 MHz spans cover the
// whole 2.400-2.4835 GHz ISM band with overlap for stitching.
void sdr_spectrum_default_config(sdr_sweep_config_t *cfg);
bool sdr_spectrum_config_valid(const sdr_sweep_config_t *cfg);

// Run one sweep (sdr_capture_begin() must already have succeeded), invoking
// `cb` once per LO in order. Yields briefly between segments. Returns the
// sweep id stamped on the segments.
uint16_t sdr_spectrum_sweep(const sdr_sweep_config_t *cfg, sdr_segment_cb_t cb, void *ctx);

#ifdef __cplusplus
}
#endif
