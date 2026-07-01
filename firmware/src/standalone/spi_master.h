// spi_master.h — primary aggregates up to 3 SPI satellites (carrier multi-radio).
// Polls each satellite's DATA_READY line, clocks a transaction, de-frames its
// CAPTURED_FRAME messages, and delivers them via a callback. Runtime-optional
// (only started when config.satellites_enabled). See docs/multi-radio.md.
#pragma once

#include <stddef.h>
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
// Non-frame messages from a satellite (e.g. MSG_OTA_STATUS) → passthrough.
typedef void (*spi_msg_cb_t)(uint8_t type, const uint8_t *payload, uint16_t len);

void spi_master_init(spi_frame_cb_t cb);
void spi_master_set_msg_cb(spi_msg_cb_t cb);

// Forward a channel change to all satellites.
void spi_master_set_channel(uint8_t channel);

// Send a fully-framed message to one satellite (0..PRI_SAT_COUNT-1) — used to
// relay OTA chunks. `data` must fit one transaction (≤ ~250 bytes framed).
void spi_master_send_to(int sat, const uint8_t *data, size_t len);

#ifdef __cplusplus
}
#endif
