// sdr_mode.h — MODE_SDR_SPECTRUM: runs the wideband SDR sweep (sdr_capture +
// sdr_spectrum) on this radio and frames each segment as MSG_SDR_SPECTRUM.
//
// This is the sniffer-specific glue: it lends the RF from the 802.15.4 capture
// driver to the Wi-Fi-PHY-based SDR for as long as the mode is selected, and
// hands it back afterward. The portable parts live in sdr_capture/sdr_spectrum.
#pragma once

#include <stdbool.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

// Sends one framed message; returns false if the transport dropped it.
typedef bool (*sdr_emit_fn)(uint8_t type, const uint8_t *payload, uint16_t len);

// Apply a CMD_SDR_CONFIG payload (safe from any task). False = malformed.
bool sdr_mode_configure(const uint8_t *p, uint16_t len);

// STATUS capability bits this radio offers (CAP_SDR when the SDR can run).
uint8_t sdr_mode_caps(void);

// Main loop, while the mode is MODE_SDR_SPECTRUM: run one sweep (lending the
// RF to the SDR first if needed), then pace to the configured interval.
void sdr_mode_run(uint8_t radio_id, sdr_emit_fn emit);

// Main loop, for any other mode: give the RF back to 802.15.4 if the SDR still
// holds it. Cheap no-op otherwise.
void sdr_mode_leave(void);

#ifdef __cplusplus
}
#endif
