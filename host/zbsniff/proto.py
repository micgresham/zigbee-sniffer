"""Wire protocol (v1) — Python mirror of firmware/src/core/codec.c.

Source of truth: /protocol/framing.md and /protocol/framing.json. This module
implements the same CRC, framing, and message layouts so the host and firmware
interoperate. Kept dependency-free for easy unit testing.
"""

from __future__ import annotations

import json
import struct
from dataclasses import dataclass
from enum import IntEnum

MAGIC0 = 0x5A
MAGIC1 = 0xBE
PROTO_VER = 1

MAX_PSDU = 127
MAX_PAYLOAD = 2048

CHANNEL_MIN = 11
CHANNEL_MAX = 26


class Msg(IntEnum):
    CAPTURED_FRAME = 0x01
    ED_RESULT = 0x02
    STATUS = 0x03
    LOG = 0x04
    ACK = 0x05
    INCIDENT = 0x06
    CMD_SET_CHANNEL = 0x81
    CMD_SET_MODE = 0x82
    CMD_START = 0x83
    CMD_STOP = 0x84
    CMD_SET_HOP = 0x85
    CMD_ED_SCAN = 0x86
    CMD_SET_KEY = 0x87
    CMD_GET_STATUS = 0x88
    CMD_SET_RADIO_ID = 0x89


class Mode(IntEnum):
    IDLE = 0
    CAPTURE = 1
    ED_SWEEP = 2
    CAPTURE_PLUS_ED = 3


FLAG_CRC_OK = 0x01
FLAG_PROMISCUOUS = 0x02
FLAG_FRAME_PENDING = 0x04
FLAG_WAS_ACKED = 0x08


def crc16(data: bytes) -> int:
    """CRC-16/CCITT-FALSE: poly 0x1021, init 0xFFFF, no reflection."""
    crc = 0xFFFF
    for b in data:
        crc ^= b << 8
        for _ in range(8):
            crc = ((crc << 1) ^ 0x1021) & 0xFFFF if (crc & 0x8000) else (crc << 1) & 0xFFFF
    return crc


def encode(msg_type: int, payload: bytes = b"") -> bytes:
    """Build a complete framed message."""
    if len(payload) > MAX_PAYLOAD:
        raise ValueError("payload too large")
    header_body = struct.pack("<BBH", PROTO_VER, msg_type, len(payload)) + payload
    crc = crc16(header_body)
    return bytes([MAGIC0, MAGIC1]) + header_body + struct.pack("<H", crc)


# --- command builders ------------------------------------------------------

def cmd_set_channel(ch: int) -> bytes:
    return encode(Msg.CMD_SET_CHANNEL, bytes([ch]))


def cmd_set_mode(mode: int) -> bytes:
    return encode(Msg.CMD_SET_MODE, bytes([mode]))


def cmd_start() -> bytes:
    return encode(Msg.CMD_START)


def cmd_stop() -> bytes:
    return encode(Msg.CMD_STOP)


def cmd_set_hop(hop_mask: int, dwell_ms: int) -> bytes:
    return encode(Msg.CMD_SET_HOP, struct.pack("<IH", hop_mask, dwell_ms))


def cmd_ed_scan(hop_mask: int, dwell_ms: int) -> bytes:
    return encode(Msg.CMD_ED_SCAN, struct.pack("<IH", hop_mask, dwell_ms))


def cmd_set_key(key: bytes | None) -> bytes:
    if key is None:
        return encode(Msg.CMD_SET_KEY, bytes([0]))
    if len(key) != 16:
        raise ValueError("network key must be 16 bytes")
    return encode(Msg.CMD_SET_KEY, bytes([1]) + key)


def cmd_get_status() -> bytes:
    return encode(Msg.CMD_GET_STATUS)


def all_channels_mask() -> int:
    return sum(1 << (ch - CHANNEL_MIN) for ch in range(CHANNEL_MIN, CHANNEL_MAX + 1))


# --- parsed message types --------------------------------------------------

@dataclass
class CapturedFrame:
    radio_id: int
    channel: int
    rssi: int
    lqi: int
    flags: int
    timestamp: int  # microseconds
    mpdu: bytes     # raw 802.15.4 MPDU, includes FCS

    @property
    def crc_ok(self) -> bool:
        return bool(self.flags & FLAG_CRC_OK)


@dataclass
class EdResult:
    radio_id: int
    channel: int
    timestamp: int
    ed_dbm: int
    sweep_id: int


@dataclass
class Status:
    radio_id: int
    mode: int
    channel: int
    hop_mask: int
    hop_dwell_ms: int
    uptime_s: int
    captured: int
    dropped_crc: int
    dropped_buf: int
    fw_major: int
    fw_minor: int


@dataclass
class LogLine:
    text: str


@dataclass
class Incident:
    raw: str          # UTF-8 JSON as emitted by the device
    data: dict        # parsed (empty dict if it failed to parse)


@dataclass
class Ack:
    cmd_type: int
    result: int


def parse_payload(msg_type: int, p: bytes):
    """Decode a payload into one of the dataclasses above (or None if unknown)."""
    if msg_type == Msg.CAPTURED_FRAME:
        radio_id, channel, rssi, lqi, flags = struct.unpack_from("<BBbBB", p, 0)
        (timestamp,) = struct.unpack_from("<Q", p, 5)
        mpdu_len = p[13]
        mpdu = p[14:14 + mpdu_len]
        return CapturedFrame(radio_id, channel, rssi, lqi, flags, timestamp, mpdu)
    if msg_type == Msg.ED_RESULT:
        radio_id, channel = p[0], p[1]
        (timestamp,) = struct.unpack_from("<Q", p, 2)
        ed_dbm = struct.unpack_from("<b", p, 10)[0]
        (sweep_id,) = struct.unpack_from("<H", p, 11)
        return EdResult(radio_id, channel, timestamp, ed_dbm, sweep_id)
    if msg_type == Msg.STATUS:
        (radio_id, mode, channel, hop_mask, hop_dwell_ms, uptime_s,
         captured, dropped_crc, dropped_buf, fw_major, fw_minor) = struct.unpack_from(
            "<BBBIHIIIIBB", p, 0)
        return Status(radio_id, mode, channel, hop_mask, hop_dwell_ms, uptime_s,
                      captured, dropped_crc, dropped_buf, fw_major, fw_minor)
    if msg_type == Msg.LOG:
        return LogLine(p.decode("utf-8", "replace"))
    if msg_type == Msg.INCIDENT:
        raw = p.decode("utf-8", "replace")
        try:
            return Incident(raw, json.loads(raw))
        except ValueError:
            return Incident(raw, {})
    if msg_type == Msg.ACK:
        return Ack(p[0], p[1])
    return None


class StreamDecoder:
    """Incremental frame decoder mirroring the C zb_decoder_t state machine."""

    def __init__(self) -> None:
        self._buf = bytearray()

    def feed(self, data: bytes):
        """Feed raw bytes; yield (msg_type, payload_bytes) for each valid frame."""
        self._buf.extend(data)
        out = []
        while True:
            frame = self._try_extract()
            if frame is None:
                break
            out.append(frame)
        return out

    def _try_extract(self):
        b = self._buf
        # find magic
        i = b.find(bytes([MAGIC0, MAGIC1]))
        if i < 0:
            # keep at most one trailing byte (could be a partial magic)
            if len(b) > 1:
                del b[:-1]
            return None
        if i > 0:
            del b[:i]
        if len(b) < 6:
            return None
        ver, mtype, plen = struct.unpack_from("<BBH", b, 2)
        if ver != PROTO_VER or plen > MAX_PAYLOAD:
            del b[:2]  # bad header, skip past this magic
            return None
        total = 6 + plen + 2
        if len(b) < total:
            return None
        body = bytes(b[2:6 + plen])
        (crc_rx,) = struct.unpack_from("<H", b, 6 + plen)
        del b[:total]
        if crc16(body) != crc_rx:
            return None
        return (mtype, body[4:])
