"""Zigbee Cluster Library (ZCL) header decode (enough for a frame summary)."""

from __future__ import annotations

from dataclasses import dataclass

ZCL_GLOBAL = 0
ZCL_CLUSTER_SPECIFIC = 1

# A few common global command ids for nicer summaries.
GLOBAL_COMMANDS = {
    0x00: "Read Attributes", 0x01: "Read Attributes Response",
    0x02: "Write Attributes", 0x04: "Write Attributes Response",
    0x06: "Configure Reporting", 0x07: "Configure Reporting Response",
    0x0A: "Report Attributes", 0x0B: "Default Response",
    0x0C: "Discover Attributes",
}


@dataclass
class ZclFrame:
    frame_type: int
    manufacturer_specific: bool
    manufacturer_code: int | None
    direction: int          # 0 = client→server, 1 = server→client
    disable_default_response: bool
    tsn: int | None
    command_id: int | None
    payload: bytes

    @property
    def summary(self) -> str:
        if self.frame_type == ZCL_GLOBAL and self.command_id in GLOBAL_COMMANDS:
            return GLOBAL_COMMANDS[self.command_id]
        kind = "cluster-cmd" if self.frame_type == ZCL_CLUSTER_SPECIFIC else "global-cmd"
        return f"{kind} 0x{self.command_id:02x}" if self.command_id is not None else "ZCL"


def decode_zcl(data: bytes) -> ZclFrame | None:
    if len(data) < 3:
        return None
    fc = data[0]
    ftype = fc & 0x3
    mfr = bool((fc >> 2) & 1)
    direction = (fc >> 3) & 1
    ddr = bool((fc >> 4) & 1)
    o = 1
    mfr_code = None
    if mfr:
        mfr_code = int.from_bytes(data[o:o + 2], "little"); o += 2
    tsn = data[o]; o += 1
    cmd = data[o]; o += 1
    return ZclFrame(ftype, mfr, mfr_code, direction, ddr, tsn, cmd, data[o:])
