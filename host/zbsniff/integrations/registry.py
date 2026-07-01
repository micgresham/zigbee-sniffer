"""Coordinator-sourced device registry (name resolution + roles)."""

from __future__ import annotations

import threading
from dataclasses import dataclass


@dataclass
class DeviceInfo:
    short: int | None       # 16-bit network address
    ieee: int | None        # 64-bit extended address
    name: str | None
    type: str | None        # coordinator / router / end_device
    source: str             # "z2m" or "zha"


class DeviceRegistry:
    """Thread-safe map of coordinator-known devices, keyed by short and IEEE addr."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self.by_short: dict[int, DeviceInfo] = {}
        self.by_ieee: dict[int, DeviceInfo] = {}

    def update(self, infos: list[DeviceInfo]) -> None:
        with self._lock:
            for i in infos:
                if i.short is not None:
                    self.by_short[i.short] = i
                if i.ieee is not None:
                    self.by_ieee[i.ieee] = i

    def resolve(self, short: int | None = None, ieee: int | None = None) -> DeviceInfo | None:
        with self._lock:
            if short is not None and short in self.by_short:
                return self.by_short[short]
            if ieee is not None and ieee in self.by_ieee:
                return self.by_ieee[ieee]
        return None

    def name_for(self, short: int | None = None, ieee: int | None = None) -> str | None:
        info = self.resolve(short, ieee)
        return info.name if info else None

    def count(self) -> int:
        with self._lock:
            return len(set(self.by_short) | {id(v) for v in self.by_ieee.values()})
