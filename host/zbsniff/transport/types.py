"""Transport data types (dependency-free, no pyserial import)."""

from __future__ import annotations

from dataclasses import dataclass


@dataclass
class Tagged:
    """A parsed message plus which port it came from and host arrival time."""
    port: str
    host_ts: float          # time.time() when the host read it
    msg_type: int
    message: object         # one of proto.CapturedFrame / EdResult / Status / ...
