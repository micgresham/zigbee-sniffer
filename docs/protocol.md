# Protocol

Two protocols are involved:

## 1. Device ↔ host wire framing

The binary framing used on **every** transport (USB serial, SPI, WebSocket) is specified in
[`protocol/framing.md`](../protocol/framing.md), with a machine-readable mirror in
[`protocol/framing.json`](../protocol/framing.json). It is the **single source of truth**.

Implementations that must stay in sync:
- `firmware/src/core/codec.c` + `proto.h` (C)
- `host/zbsniff/proto.py` (Python)
- the browser client decoder (Block B)

Summary: `0x5A 0xBE | ver | type | len(u16) | payload | crc16` with CRC-16/CCITT-FALSE. Message
types cover `CAPTURED_FRAME`, `ED_RESULT`, `STATUS`, `LOG`, `ACK`, and host→device commands.

## 2. pcap export (Wireshark)

For Wireshark we emit classic pcap with **`LINKTYPE_IEEE802_15_4_TAP` (283)**. Each packet is a
TAP pseudo-header (4-byte base + 4-byte-aligned TLVs) followed by the raw MPDU.

TLVs we emit (`host/zbsniff/store/pcap.py`):

| TLV | Type | Value |
|-----|------|-------|
| FCS type | 0 | u8: 0=none, 1=16-bit CRC |
| RSS | 1 | float32 dBm (RSSI) |
| Channel assignment | 3 | u16 channel + u8 page |
| LQI | 10 | u8 |

This gives Wireshark RSSI/LQI/channel natively, no custom dissector. Encrypted Zigbee payloads
remain encrypted unless a key is supplied (Wireshark's Zigbee protocol preferences, or the host
decryptor — Block C; see [decoding-and-decryption.md](decoding-and-decryption.md)).
