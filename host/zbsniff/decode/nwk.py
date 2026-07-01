"""Zigbee NWK (network) layer decode, with optional AES-CCM* decryption.

Parses the NWK header (addressing, radius, sequence, optional IEEE addresses,
source-route subframe, and the auxiliary security header). If the frame is
secured and a network key is supplied, decrypts the payload in place.

Mirrors the structure used by zigpy/Wireshark; see docs/decoding-and-decryption.md.
"""

from __future__ import annotations

from dataclasses import dataclass

from . import crypto

# NWK frame-control bit fields (16-bit little-endian)
FT_DATA = 0
FT_COMMAND = 1


@dataclass
class NwkFrame:
    frame_type: int
    version: int
    multicast: bool
    secure: bool
    source_route: bool
    dst: int | None
    src: int | None
    radius: int | None
    seq: int | None
    dst_ieee: int | None
    src_ieee: int | None
    relays: list[int]
    # security
    sec_frame_counter: int | None
    sec_src_ext: int | None
    sec_key_seq: int | None
    decrypted: bool
    payload: bytes          # decrypted NWK payload (APS layer) if decrypted else raw/encrypted

    @property
    def type_name(self) -> str:
        return {FT_DATA: "NWK Data", FT_COMMAND: "NWK Command"}.get(self.frame_type, "NWK?")


def decode_nwk(data: bytes, network_key: bytes | None = None) -> NwkFrame | None:
    """Decode an NWK frame (the MAC payload of a Data frame). None if too short."""
    if len(data) < 8:
        return None
    fcf = data[0] | (data[1] << 8)
    ftype = fcf & 0x3
    version = (fcf >> 2) & 0xF
    multicast = bool((fcf >> 8) & 1)
    secure = bool((fcf >> 9) & 1)
    src_route = bool((fcf >> 10) & 1)
    dst_ieee_present = bool((fcf >> 11) & 1)
    src_ieee_present = bool((fcf >> 12) & 1)

    o = 2
    dst = int.from_bytes(data[o:o + 2], "little"); o += 2
    src = int.from_bytes(data[o:o + 2], "little"); o += 2
    radius = data[o]; o += 1
    seq = data[o]; o += 1

    dst_ieee = src_ieee = None
    if dst_ieee_present:
        dst_ieee = int.from_bytes(data[o:o + 8], "little"); o += 8
    if src_ieee_present:
        src_ieee = int.from_bytes(data[o:o + 8], "little"); o += 8
    if multicast:
        o += 1  # multicast control
    relays: list[int] = []
    if src_route:
        relay_count = data[o]; o += 1
        o += 1  # relay index
        for _ in range(relay_count):
            relays.append(int.from_bytes(data[o:o + 2], "little")); o += 2

    sec_fc = sec_src_ext = sec_key_seq = None
    decrypted = False
    payload = data[o:]

    if secure:
        aux_start = o
        sec_control = data[o]; o += 1
        key_id = (sec_control >> 3) & 0x3
        ext_nonce = bool((sec_control >> 5) & 1)
        sec_fc = int.from_bytes(data[o:o + 4], "little"); o += 4
        if ext_nonce:
            sec_src_ext = int.from_bytes(data[o:o + 8], "little"); o += 8
        if key_id == 1:  # network key
            sec_key_seq = data[o]; o += 1
        aux_end = o
        enc_payload_and_mic = data[aux_end:]
        payload = enc_payload_and_mic  # default: leave encrypted

        if network_key is not None and sec_src_ext is not None:
            # Build AAD = NWK header through aux header, with the security level
            # restored to the real over-the-air level (5).
            real_sc = crypto.restore_sec_level(sec_control)
            aad = bytearray(data[:aux_end])
            aad[aux_start] = real_sc
            nonce = crypto.make_nonce(sec_src_ext, sec_fc, real_sc)
            pt = crypto.decrypt_ccm(network_key, nonce, bytes(aad), enc_payload_and_mic,
                                    mic_len=4)
            if pt is not None:
                payload = pt
                decrypted = True

    return NwkFrame(ftype, version, multicast, secure, src_route, dst, src, radius, seq,
                    dst_ieee, src_ieee, relays, sec_fc, sec_src_ext, sec_key_seq,
                    decrypted, payload)
