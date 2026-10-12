// sdr_capture.h — raw baseband I/Q snapshots from the ESP32-C6's 2.4 GHz
// receiver (the "SDR" path behind the wideband spectrum analyzer).
//
// The C6's Wi-Fi modem has an undocumented sample-dump engine that writes the
// receiver's raw ADC output — signed 10-bit I and Q at 80 MS/s — into an
// internal SRAM bank. This module drives it directly: it brings the Wi-Fi PHY
// up in a receive-only state, tunes the LO, and takes bounded snapshots of up
// to SDR_MAX_SAMPLES complex samples (~200 us of RF).
//
// The existence of this path, its register map and sample format were
// published by the ESPARGOS project (https://espargos.net/espsdr/). This is an
// independent implementation; no ESPARGOS source code is included.
//
// Self-contained on purpose (no zigbee-sniffer protocol dependencies) so it can
// be lifted into other ESP32-C6 projects. Enable with -DSDR_ENABLE=1; the build
// must NOT otherwise use Wi-Fi (sdr_capture_begin() owns esp_wifi while active)
// and loses SDR_BANK_SIZE bytes of heap to the dump buffer.
#pragma once

#include <stdbool.h>
#include <stdint.h>
#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

#define SDR_SAMPLE_RATE_HZ 80000000u   // complex samples per second (fixed on C6)
#define SDR_MAX_SAMPLES    16128u      // 63 x 256; leaves guard words in the 64 KiB window
#define SDR_TUNE_MIN_MHZ   2300u       // conservative PLL envelope for this chip
#define SDR_TUNE_MAX_MHZ   2600u

// Sample word layout (one 32-bit word per complex sample):
//   bits 31..28  AGC state (undocumented)
//   bits 27..20  RX gain index in effect for this sample
//   bits 19..10  I, signed 10-bit
//   bits  9..0   Q, signed 10-bit
static inline int sdr_sample_i(uint32_t w) { return ((int32_t)(w << 12)) >> 22; }
static inline int sdr_sample_q(uint32_t w) { return ((int32_t)(w << 22)) >> 22; }
static inline unsigned sdr_sample_gain(uint32_t w) { return (w >> 20) & 0xFFu; }

// True when this build can capture (compiled with SDR_ENABLE on an ESP32-C6,
// and the static RAM image ends below the dump bank). When false,
// sdr_capture_unsupported_reason() says why.
bool sdr_capture_supported(void);
const char *sdr_capture_unsupported_reason(void);

// Bring the Wi-Fi PHY up receive-only (NULL mode, promiscuous, power-save off).
// The caller must have released the radio from any other user (e.g. parked and
// disabled the 802.15.4 driver) first. Idempotent.
esp_err_t sdr_capture_begin(void);

// Tear the Wi-Fi PHY down again and restore the modem registers the capture
// path touches. Idempotent.
void sdr_capture_end(void);

bool sdr_capture_active(void);

// Retune the receiver LO to `mhz` and let it settle. Wi-Fi channel centers
// (2412..2472 step 5, 2484) use the PHY's calibrated channel path; any other
// frequency in [SDR_TUNE_MIN_MHZ, SDR_TUNE_MAX_MHZ] is programmed directly.
bool sdr_capture_tune(uint16_t mhz);

// Capture `n` (<= SDR_MAX_SAMPLES) consecutive complex samples. On success
// *out points at n sample words, valid until the next snapshot. Fails on a
// hardware timeout or if the engine wrote short/past the requested length.
bool sdr_capture_snapshot(uint32_t n, const uint32_t **out);

#ifdef __cplusplus
}
#endif
