"""ZHA integration over the Home Assistant WebSocket API.

Authenticates with a long-lived access token and pulls the ZHA device list
(`zha/devices`). The parse function is pure (testable); the client uses the
`websockets` library (optional `host` extra).
"""

from __future__ import annotations

import json

from .registry import DeviceInfo, DeviceRegistry

_ZHA_TYPE = {"Coordinator": "coordinator", "Router": "router",
             "EndDevice": "end_device", "Mains": "router"}


def _ieee_to_int(ieee: str | None) -> int | None:
    if not ieee:
        return None
    try:
        return int(ieee.replace(":", ""), 16)
    except ValueError:
        return None


def parse_zha_devices(devices: list[dict]) -> list[DeviceInfo]:
    """Parse a HA `zha/devices` result into DeviceInfo list."""
    out: list[DeviceInfo] = []
    for d in devices:
        nwk = d.get("nwk")
        if isinstance(nwk, str):
            try:
                nwk = int(nwk, 16)
            except ValueError:
                nwk = None
        out.append(DeviceInfo(
            short=nwk, ieee=_ieee_to_int(d.get("ieee")),
            name=d.get("user_given_name") or d.get("name"),
            type=_ZHA_TYPE.get(d.get("device_type")),
            source="zha"))
    return out


class ZHAClient:
    """Async HA-WebSocket client that keeps a DeviceRegistry up to date from ZHA."""

    def __init__(self, registry: DeviceRegistry, url: str, token: str):
        # url like ws://homeassistant.local:8123/api/websocket
        self.registry = registry
        self.url = url
        self.token = token

    async def run(self) -> None:
        import websockets  # optional dep (host extra)
        async with websockets.connect(self.url) as ws:
            await ws.recv()  # auth_required
            await ws.send(json.dumps({"type": "auth", "access_token": self.token}))
            await ws.recv()  # auth_ok / auth_invalid
            await ws.send(json.dumps({"id": 1, "type": "zha/devices"}))
            while True:
                msg = json.loads(await ws.recv())
                if msg.get("id") == 1 and msg.get("type") == "result":
                    self.registry.update(parse_zha_devices(msg.get("result", [])))
                    break
