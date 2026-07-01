"""Write captured frames as a pcap stream using LINKTYPE_IEEE802_15_4_TAP (283).

This matches the format Wireshark dissects natively with per-packet RSSI, LQI,
and channel — the same approach the reference ESP32 sniffers use, so captures
open directly in Wireshark with no custom dissector.

TAP reference: the IEEE 802.15.4 TAP pseudo-header is a small fixed header
followed by 4-byte-aligned TLVs, then the PHY payload (MPDU). See
docs/protocol.md for the TLV layout we emit.
"""

from __future__ import annotations

import struct
from typing import BinaryIO

LINKTYPE_IEEE802_15_4_TAP = 283

# TAP TLV types we emit (subset of the Exegin/Wireshark definitions).
TLV_FCS_TYPE = 0      # u8: 0=none, 1=16-bit CRC, 2=32-bit CRC
TLV_RSS = 1           # float32 dBm
TLV_CHANNEL = 3       # u16 channel + u8 page
TLV_LQI = 10          # u8


def _tlv(tlv_type: int, value: bytes) -> bytes:
    """One TLV: type(u16) len(u16) value, padded to a 4-byte boundary."""
    hdr = struct.pack("<HH", tlv_type, len(value))
    body = hdr + value
    pad = (-len(body)) % 4
    return body + (b"\x00" * pad)


def build_tap_header(rssi_dbm: int, lqi: int, channel: int, fcs_present: bool) -> bytes:
    """Build the IEEE 802.15.4 TAP pseudo-header with our metadata TLVs."""
    tlvs = b""
    tlvs += _tlv(TLV_FCS_TYPE, struct.pack("<B", 1 if fcs_present else 0))
    tlvs += _tlv(TLV_RSS, struct.pack("<f", float(rssi_dbm)))
    tlvs += _tlv(TLV_CHANNEL, struct.pack("<HB", channel, 0))
    tlvs += _tlv(TLV_LQI, struct.pack("<B", lqi))
    # Base header: version(u8)=0, reserved(u8)=0, length(u16)=whole TAP header.
    total_len = 4 + len(tlvs)
    base = struct.pack("<BBH", 0, 0, total_len)
    return base + tlvs


class PcapWriter:
    """Minimal classic-pcap writer for the 802.15.4 TAP link type."""

    def __init__(self, fp: BinaryIO, fcs_present: bool = True):
        self.fp = fp
        self.fcs_present = fcs_present
        self._write_global_header()

    def _write_global_header(self) -> None:
        # magic, version 2.4, thiszone, sigfigs, snaplen, network
        self.fp.write(struct.pack(
            "<IHHiIII",
            0xA1B2C3D4, 2, 4, 0, 0, 65535, LINKTYPE_IEEE802_15_4_TAP))
        self.fp.flush()

    def write_frame(self, ts_us: int, rssi: int, lqi: int, channel: int, mpdu: bytes) -> None:
        tap = build_tap_header(rssi, lqi, channel, self.fcs_present)
        packet = tap + mpdu
        ts_sec = ts_us // 1_000_000
        ts_usec = ts_us % 1_000_000
        self.fp.write(struct.pack("<IIII", ts_sec, ts_usec, len(packet), len(packet)))
        self.fp.write(packet)
        self.fp.flush()
