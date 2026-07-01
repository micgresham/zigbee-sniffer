// codec.c — see codec.h.
#include "codec.h"
#include <string.h>

// CRC-16/CCITT-FALSE: poly 0x1021, init 0xFFFF, no reflection, xorout 0x0000.
uint16_t zb_crc16(const uint8_t *data, size_t len)
{
    uint16_t crc = 0xFFFF;
    for (size_t i = 0; i < len; i++) {
        crc ^= (uint16_t)data[i] << 8;
        for (int b = 0; b < 8; b++) {
            crc = (crc & 0x8000) ? (uint16_t)((crc << 1) ^ 0x1021) : (uint16_t)(crc << 1);
        }
    }
    return crc;
}

size_t zb_encode(uint8_t type, const uint8_t *payload, uint16_t payload_len,
                 uint8_t *out, size_t out_cap)
{
    if (payload_len > ZB_MAX_PAYLOAD) return 0;
    size_t total = ZB_FRAME_OVERHEAD + payload_len;
    if (out_cap < total) return 0;

    out[0] = ZB_MAGIC0;
    out[1] = ZB_MAGIC1;
    out[2] = PROTO_VER;
    out[3] = type;
    out[4] = (uint8_t)(payload_len & 0xFF);
    out[5] = (uint8_t)(payload_len >> 8);
    if (payload_len && payload) {
        memcpy(&out[6], payload, payload_len);
    }
    // CRC covers ver,type,len,payload (bytes 2 .. 6+payload_len).
    uint16_t crc = zb_crc16(&out[2], 4 + payload_len);
    out[6 + payload_len] = (uint8_t)(crc & 0xFF);
    out[7 + payload_len] = (uint8_t)(crc >> 8);
    return total;
}

size_t zb_encode_captured(uint8_t radio_id, const captured_frame_t *f,
                          uint8_t *out, size_t out_cap)
{
    // payload: radio_id,channel,rssi,lqi,flags,timestamp(8),mpdu_len,mpdu
    uint8_t p[5 + 8 + 1 + ZB_MAX_PSDU];
    size_t n = 0;
    p[n++] = radio_id;
    p[n++] = f->channel;
    p[n++] = (uint8_t)f->rssi;
    p[n++] = f->lqi;
    p[n++] = f->flags;
    for (int i = 0; i < 8; i++) p[n++] = (uint8_t)(f->timestamp >> (8 * i));
    p[n++] = f->len;
    memcpy(&p[n], f->psdu, f->len);
    n += f->len;
    return zb_encode(MSG_CAPTURED_FRAME, p, (uint16_t)n, out, out_cap);
}

void zb_decoder_init(zb_decoder_t *d)
{
    memset(d, 0, sizeof(*d));
    d->state = S_M0;
}

bool zb_decoder_push(zb_decoder_t *d, uint8_t byte,
                     uint8_t *out_type, const uint8_t **out_payload, uint16_t *out_len)
{
    switch (d->state) {
    case S_M0:
        if (byte == ZB_MAGIC0) d->state = S_M1;
        break;
    case S_M1:
        d->state = (byte == ZB_MAGIC1) ? S_VER : S_M0;
        if (byte == ZB_MAGIC0) d->state = S_M1;  // tolerate 0x5A 0x5A 0xBE
        break;
    case S_VER:
        d->buf[0] = byte;            // store ver at buf[0] for CRC region
        d->state = (byte == PROTO_VER) ? S_TYPE : S_M0;
        break;
    case S_TYPE:
        d->buf[1] = byte;
        d->type = byte;
        d->state = S_LEN0;
        break;
    case S_LEN0:
        d->buf[2] = byte;
        d->payload_len = byte;
        d->state = S_LEN1;
        break;
    case S_LEN1:
        d->buf[3] = byte;
        d->payload_len |= (uint16_t)byte << 8;
        if (d->payload_len > ZB_MAX_PAYLOAD) { d->state = S_M0; break; }
        d->idx = 4;                  // ver,type,len already in buf[0..3]
        d->state = d->payload_len ? S_PAYLOAD : S_CRC0;
        break;
    case S_PAYLOAD:
        d->buf[d->idx++] = byte;
        if (d->idx >= (size_t)(4 + d->payload_len)) d->state = S_CRC0;
        break;
    case S_CRC0:
        d->crc_rx = byte;
        d->state = S_CRC1;
        break;
    case S_CRC1: {
        d->crc_rx |= (uint16_t)byte << 8;
        uint16_t calc = zb_crc16(d->buf, 4 + d->payload_len);
        d->state = S_M0;
        if (calc == d->crc_rx) {
            *out_type = d->type;
            *out_payload = &d->buf[4];
            *out_len = d->payload_len;
            return true;
        }
        break;
    }
    }
    return false;
}
