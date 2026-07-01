"""Zigbee APS (application support sub-layer) decode — common data frames."""

from __future__ import annotations

from dataclasses import dataclass

APS_DATA = 0
APS_COMMAND = 1
APS_ACK = 2

DELIVERY_UNICAST = 0
DELIVERY_BROADCAST = 2
DELIVERY_GROUP = 3


@dataclass
class ApsFrame:
    frame_type: int
    delivery_mode: int
    secure: bool
    dst_endpoint: int | None
    group: int | None
    cluster: int | None
    profile: int | None
    src_endpoint: int | None
    counter: int | None
    payload: bytes

    @property
    def type_name(self) -> str:
        return {APS_DATA: "APS Data", APS_COMMAND: "APS Cmd",
                APS_ACK: "APS Ack"}.get(self.frame_type, "APS?")


def decode_aps(data: bytes) -> ApsFrame | None:
    if len(data) < 1:
        return None
    fc = data[0]
    ftype = fc & 0x3
    delivery = (fc >> 2) & 0x3
    secure = bool((fc >> 5) & 1)
    ext_header = bool((fc >> 7) & 1)

    o = 1
    dst_ep = group = cluster = profile = src_ep = counter = None

    if ftype == APS_DATA:
        if delivery == DELIVERY_GROUP:
            group = int.from_bytes(data[o:o + 2], "little"); o += 2
        else:
            if o < len(data):
                dst_ep = data[o]; o += 1
        cluster = int.from_bytes(data[o:o + 2], "little"); o += 2
        profile = int.from_bytes(data[o:o + 2], "little"); o += 2
        if o < len(data):
            src_ep = data[o]; o += 1
    if o < len(data):
        counter = data[o]; o += 1
    if ext_header and o < len(data):
        o += 1  # extended header (fragmentation) — skipped

    payload = data[o:]
    return ApsFrame(ftype, delivery, secure, dst_ep, group, cluster, profile,
                    src_ep, counter, payload)
