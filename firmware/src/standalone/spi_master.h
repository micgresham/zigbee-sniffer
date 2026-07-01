// spi_master.h — primary aggregates up to 3 SPI satellites (carrier multi-radio).
// Polls each satellite's DATA_READY line, clocks a transaction, de-frames its
// CAPTURED_FRAME messages, and delivers them via a callback. Runtime-optional
// (only started when config.satellites_enabled). See docs/multi-radio.md.
#pragma once

#include <stdint.h>
#include "proto.h"

#ifdef __cplusplus
extern "C" {
#endif

#define PRI_SAT_COUNT 3
#define PRI_PIN_SCK   6
#define PRI_PIN_MOSI  7
#define PRI_PIN_MISO  2
// per-satellite chip-select and data-ready lines, plus a shared SYNC output
#define PRI_CS_PINS    {10, 18, 19}
#define PRI_DREADY_PINS {20, 21, 22}
#define PRI_PIN_SYNC   23

typedef void (*spi_frame_cb_t)(uint8_t radio_id, const captured_frame_t *cf);

void spi_master_init(spi_frame_cb_t cb);

// Forward a channel change to all satellites.
void spi_master_set_channel(uint8_t channel);

#ifdef __cplusplus
}
#endif
