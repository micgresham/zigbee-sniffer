// proto.h — wire protocol constants shared by all builds.
// MUST stay in sync with /protocol/framing.md and /protocol/framing.json.
#pragma once

#include <stdint.h>

#define ZB_MAGIC0 0x5A
#define ZB_MAGIC1 0xBE
#ifndef PROTO_VER
#define PROTO_VER 1
#endif

#define ZB_MAX_PSDU 127             // IEEE 802.15.4 max PHY payload
#define ZB_MAX_PAYLOAD 2048         // max framing payload (protocol limit)
#define ZB_HEADER_LEN 6             // magic0,magic1,ver,type,len(2)
#define ZB_CRC_LEN 2
#define ZB_FRAME_OVERHEAD (ZB_HEADER_LEN + ZB_CRC_LEN)

// Right-sized buffer for messages the firmware *originates* (captured frames,
// ED, status, incidents). The largest is a CAPTURED_FRAME (~14 meta + 127 PSDU)
// or an incident JSON (~224 B). 256 B payload covers all; using ZB_MAX_PAYLOAD
// here would put a ~2 KB buffer on the stack and overflow the main task.
#define ZB_MAX_TX (ZB_FRAME_OVERHEAD + 256)

// Message types — device -> host (0x0_)
typedef enum {
    MSG_CAPTURED_FRAME = 0x01,
    MSG_ED_RESULT      = 0x02,
    MSG_STATUS         = 0x03,
    MSG_LOG            = 0x04,
    MSG_ACK            = 0x05,
    MSG_INCIDENT       = 0x06,   // payload: UTF-8 JSON (see docs/incidents.md)
    MSG_PROBE_RESULT   = 0x07,   // radio_id(1) target(2) acked(1) rssi(i8) lqi(1)
    MSG_OTA_STATUS     = 0x08,   // target(1) state(1) received(4) total(4) err(1)
    // host -> device (0x8_)
    CMD_SET_CHANNEL    = 0x81,
    CMD_SET_MODE       = 0x82,
    CMD_START          = 0x83,
    CMD_STOP           = 0x84,
    CMD_SET_HOP        = 0x85,
    CMD_ED_SCAN        = 0x86,
    CMD_SET_KEY        = 0x87,
    CMD_GET_STATUS     = 0x88,
    CMD_SET_RADIO_ID   = 0x89,
    CMD_PROBE          = 0x8A,   // active test: target(2) pan(2) — TX a MAC frame, await ACK
    CMD_OTA_BEGIN      = 0x8B,   // target(1) total_size(4) img_crc32(4)
    CMD_OTA_DATA       = 0x8C,   // target(1) offset(4) chunk(n)
    CMD_OTA_END        = 0x8D,   // target(1)
    CMD_OTA_ABORT      = 0x8E,   // target(1)
    CMD_BEACON_REQ     = 0x8F,   // active scan: TX an 802.15.4 beacon request (no payload)
    CMD_TX_RAW         = 0x90,   // transmit a host-built raw MPDU (radio appends FCS)
    // Relayed satellite control (tethered/standalone primary only): the primary
    // strips target(1) and forwards the plain inner command (CMD_SET_CHANNEL /
    // CMD_START / CMD_STOP) to satellite `target` over SPI — a satellite has no
    // serial port of its own to address directly. No mode/hop/ED equivalent:
    // satellite firmware doesn't support those.
    CMD_SAT_SET_CHANNEL = 0x91, // target(1) channel(1)
    CMD_SAT_START       = 0x92, // target(1)
    CMD_SAT_STOP        = 0x93, // target(1)
} zb_msg_type_t;

// OTA target: 0 = this (tethered) C6, 1..3 = satellite over SPI.
// OTA state (MSG_OTA_STATUS.state).
enum { OTA_IDLE = 0, OTA_RECEIVING, OTA_WRITING, OTA_VERIFYING, OTA_OK, OTA_ERROR };

// Set on a relayed satellite's CAPTURED_FRAME/STATUS radio_id byte by the
// primary ONLY when that satellite's radio_id collides with the primary's own
// (e.g. both left at their default) — without it the two are indistinguishable
// on the wire and silently merge into one radio host-side. The low 7 bits are
// still the satellite's real radio_id (1..3); mask this bit off to display it.
// Fix the collision by giving the satellite a unique radio_id.
#define RADIO_ID_COLLISION_BIT 0x80

// Capture/operating mode (see framing.md `mode` enum)
typedef enum {
    MODE_IDLE            = 0,
    MODE_CAPTURE         = 1,
    MODE_ED_SWEEP        = 2,
    MODE_CAPTURE_PLUS_ED = 3,
} zb_mode_t;

// CAPTURED_FRAME flags
#define FLAG_CRC_OK        0x01
#define FLAG_PROMISCUOUS   0x02
#define FLAG_FRAME_PENDING 0x04
#define FLAG_WAS_ACKED     0x08

#define ZB_CHANNEL_MIN 11
#define ZB_CHANNEL_MAX 26

// A captured frame as it flows from the radio callback to the transport task.
typedef struct {
    uint8_t  len;                 // PSDU length (includes 2-byte FCS)
    int8_t   rssi;                // dBm
    uint8_t  lqi;                 // 0..255
    uint8_t  channel;             // 11..26
    uint8_t  flags;               // FLAG_*
    uint64_t timestamp;           // microseconds
    uint8_t  psdu[ZB_MAX_PSDU];
} captured_frame_t;
