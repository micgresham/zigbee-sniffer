// radio_capture.h — thin wrapper over esp_ieee802154 promiscuous RX.
#pragma once

#include <stdint.h>
#include <stdbool.h>
#include "proto.h"
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"

#ifdef __cplusplus
extern "C" {
#endif

// Initialise the radio in promiscuous mode on `channel`. Captured frames are
// pushed to an internal FreeRTOS queue; pop them with radio_capture_recv().
// Frames that don't fit (queue full) increment the drop counter.
void radio_capture_init(uint8_t channel, uint8_t radio_id);

// Block up to `timeout_ms` for the next captured frame. Returns true on success.
bool radio_capture_recv(captured_frame_t *out, uint32_t timeout_ms);

void radio_capture_set_channel(uint8_t channel);
uint8_t radio_capture_get_channel(void);

void radio_capture_start(void);  // begin receiving
void radio_capture_stop(void);   // stop receiving

// Hand the RF to another user (the wideband SDR) and take it back. Call with
// radio_lock() held. While suspended, start/stop/set_channel just record the
// requested state; resume re-enables the driver and applies it. Anything that
// drives the 802.15.4 HAL directly (ED, probe, beacon, raw TX) must check
// radio_capture_suspended() first.
void radio_capture_suspend(void);
void radio_capture_resume(void);
bool radio_capture_suspended(void);

uint32_t radio_capture_count(void);        // frames captured
uint32_t radio_capture_dropped_buf(void);  // frames dropped (queue full)

// Serialize radio access. The 802.15.4 HAL is NOT safe to drive from two tasks
// at once (e.g. the command task starting an ED scan or TX while the main loop
// is mid-sweep) — concurrent access hangs the radio state machine. Any code path
// on a task OTHER than the one that owns the capture loop must wrap its radio
// operations (ed_sweep, probe/beacon TX, channel/start/stop) in these. Safe to
// call before radio_capture_init(); it lazily creates the mutex.
void radio_lock(void);
void radio_unlock(void);

#ifdef __cplusplus
}
#endif
