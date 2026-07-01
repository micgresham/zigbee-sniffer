"""IEEE 802.15.4 MAC header decoder.

Decodes the MAC frame control, sequence number, and addressing — enough for
link-quality/timing/retry diagnostics and for keying device aggregation. NWK/APS/
ZCL decode and AES-CCM* decryption build on this in later Block C work. Mirrors
the JS decoder in firmware/web-lite/src/app.js.
"""

from __future__ import annotations

from dataclasses import dataclass

FRAME_TYPES = ["Beacon", "Data", "Ack", "MAC Cmd", "Reserved4",
               "Reserved5", "Reserved6", "Reserved7"]

ADDR_NONE = 0
ADDR_SHORT = 2
ADDR_EXTENDED = 3


@dataclass
class MacFrame:
    frame_type: int
    type_name: str
    security_enabled: bool
    frame_pending: bool
    ack_request: bool
    pan_id_compression: bool
    seq: int | None
    dst_pan: int | None
    dst_addr: int | None        # 16-bit short or 64-bit extended (int)
    dst_mode: int
    src_pan: int | None
    src_addr: int | None
    src_mode: int
    payload: bytes              # MAC payload (NWK layer for Data frames)

    @property
    def summary(self) -> str:
        s = f"{self.type_name} seq={self.seq}"
        if self.src_addr is not None or self.dst_addr is not None:
            s += f" {_fmt(self.src_addr, self.src_mode)}→{_fmt(self.dst_addr, self.dst_mode)}"
        return s


def _fmt(addr: int | None, mode: int) -> str:
    if addr is None:
        return "—"
    return f"0x{addr:04x}" if mode == ADDR_SHORT else f"0x{addr:016x}"


def decode_mac(mpdu: bytes) -> MacFrame:
    """Decode a raw 802.15.4 MPDU. Tolerant of truncation (fields become None)."""
    if len(mpdu) < 3:
        return MacFrame(0, "(runt)", False, False, False, False,
                        None, None, None, 0, None, None, 0, b"")

    fcf = mpdu[0] | (mpdu[1] << 8)
    ftype = fcf & 0x7
    sec = bool((fcf >> 3) & 1)
    pending = bool((fcf >> 4) & 1)
    ack_req = bool((fcf >> 5) & 1)
    pan_comp = bool((fcf >> 6) & 1)
    dst_mode = (fcf >> 10) & 0x3
    src_mode = (fcf >> 14) & 0x3

    o = 2
    seq = mpdu[o]
    o += 1

    dst_pan = dst_addr = src_pan = src_addr = None

    def rd(n: int):
        nonlocal o
        v = int.from_bytes(mpdu[o:o + n], "little")
        o += n
        return v

    if dst_mode in (ADDR_SHORT, ADDR_EXTENDED):
        if o + 2 <= len(mpdu):
            dst_pan = rd(2)
        width = 2 if dst_mode == ADDR_SHORT else 8
        if o + width <= len(mpdu):
            dst_addr = rd(width)

    if src_mode in (ADDR_SHORT, ADDR_EXTENDED):
        if not pan_comp and o + 2 <= len(mpdu):
            src_pan = rd(2)
        elif pan_comp:
            src_pan = dst_pan
        width = 2 if src_mode == ADDR_SHORT else 8
        if o + width <= len(mpdu):
            src_addr = rd(width)

    # Remaining bytes are the MAC payload, minus the 2-byte FCS if present.
    payload = mpdu[o:-2] if len(mpdu) - o >= 2 else b""

    return MacFrame(ftype, FRAME_TYPES[ftype], sec, pending, ack_req, pan_comp,
                    seq, dst_pan, dst_addr, dst_mode, src_pan, src_addr, src_mode, payload)
