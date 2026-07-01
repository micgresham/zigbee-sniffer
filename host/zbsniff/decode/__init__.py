"""Frame decoding: MAC → NWK → APS → ZCL, with optional AES-CCM* decryption."""

from __future__ import annotations

from dataclasses import dataclass

from .mac import MacFrame, decode_mac
from .nwk import NwkFrame, decode_nwk
from .aps import ApsFrame, decode_aps
from .zcl import ZclFrame, decode_zcl

__all__ = [
    "MacFrame", "decode_mac", "NwkFrame", "decode_nwk",
    "ApsFrame", "decode_aps", "ZclFrame", "decode_zcl",
    "DecodedFrame", "decode_frame",
]


@dataclass
class DecodedFrame:
    mac: MacFrame
    nwk: NwkFrame | None = None
    aps: ApsFrame | None = None
    zcl: ZclFrame | None = None

    @property
    def summary(self) -> str:
        parts = [self.mac.summary]
        if self.nwk:
            parts.append(self.nwk.type_name + (" 🔓" if self.nwk.decrypted else ""))
        if self.aps and self.aps.cluster is not None:
            parts.append(f"cl=0x{self.aps.cluster:04x}")
        if self.zcl:
            parts.append(self.zcl.summary)
        return " · ".join(parts)


def decode_frame(mpdu: bytes, network_key: bytes | None = None) -> DecodedFrame:
    """Decode a raw 802.15.4 MPDU through as many layers as possible.

    Only MAC Data frames carry a NWK layer; decryption is attempted when the
    frame is secured and `network_key` is provided. APS/ZCL are decoded only
    when the NWK payload was successfully decrypted (otherwise it's ciphertext).
    """
    mac = decode_mac(mpdu)
    out = DecodedFrame(mac=mac)
    if mac.type_name != "Data" or not mac.payload:
        return out

    nwk = decode_nwk(mac.payload, network_key)
    out.nwk = nwk
    if not nwk or (nwk.secure and not nwk.decrypted):
        return out

    aps = decode_aps(nwk.payload)
    out.aps = aps
    if aps and aps.frame_type == 0 and aps.payload:  # APS Data
        out.zcl = decode_zcl(aps.payload)
    return out
