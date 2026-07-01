// ed_scan.h — energy-detect sweep (the RF spectrum / interference primitive).
#pragma once

#include <stdint.h>
#include <stdbool.h>

#ifdef __cplusplus
extern "C" {
#endif

// Result of one energy-detect measurement on a channel.
typedef struct {
    uint8_t  channel;   // 11..26
    int8_t   ed_dbm;    // measured energy, dBm
    uint16_t sweep_id;  // increments per full sweep
    uint64_t timestamp; // microseconds
} ed_sample_t;

// Perform a blocking energy-detect on `channel` for `duration_us`. Returns the
// measured energy in dBm. Must not run concurrently with active capture on the
// same radio (the radio can only do one thing at a time).
int8_t ed_measure(uint8_t channel, uint32_t duration_us);

// Sweep every channel set in `hop_mask` (bit i => channel 11+i), measuring each
// for `dwell_us`, invoking `cb` per sample. Returns the sweep_id used.
typedef void (*ed_sample_cb_t)(const ed_sample_t *sample, void *ctx);
uint16_t ed_sweep(uint32_t hop_mask, uint32_t dwell_us, ed_sample_cb_t cb, void *ctx);

#ifdef __cplusplus
}
#endif
