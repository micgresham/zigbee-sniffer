// codec.h — framing encode/decode + CRC-16/CCITT-FALSE.
// Pure C, no IDF deps, so it can be unit-tested on the host too.
#pragma once

#include <stddef.h>
#include <stdint.h>
#include <stdbool.h>
#include "proto.h"

#ifdef __cplusplus
extern "C" {
#endif

uint16_t zb_crc16(const uint8_t *data, size_t len);

// Encode a complete framed message into `out`.
// Returns the total number of bytes written, or 0 if `out_cap` is too small.
size_t zb_encode(uint8_t type, const uint8_t *payload, uint16_t payload_len,
                 uint8_t *out, size_t out_cap);

// Convenience: encode a CAPTURED_FRAME message from a captured_frame_t.
size_t zb_encode_captured(uint8_t radio_id, const captured_frame_t *f,
                          uint8_t *out, size_t out_cap);

// Incremental decoder: feed bytes one at a time; on a complete, CRC-valid
// frame the callback fires with the message type and payload.
typedef struct {
    uint8_t  buf[ZB_FRAME_OVERHEAD + ZB_MAX_PAYLOAD];
    size_t   idx;        // bytes collected
    uint16_t payload_len;
    enum { S_M0, S_M1, S_VER, S_TYPE, S_LEN0, S_LEN1, S_PAYLOAD, S_CRC0, S_CRC1 } state;
    uint8_t  type;
    uint16_t crc_rx;
} zb_decoder_t;

void zb_decoder_init(zb_decoder_t *d);

// Returns true and sets *out_type/*out_payload/*out_len when a valid frame
// completes on this byte. Caller must consume the payload before the next call.
bool zb_decoder_push(zb_decoder_t *d, uint8_t byte,
                     uint8_t *out_type, const uint8_t **out_payload, uint16_t *out_len);

#ifdef __cplusplus
}
#endif
