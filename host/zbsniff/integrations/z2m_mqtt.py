"""Zigbee2MQTT integration over MQTT.

Subscribes to `<base>/bridge/devices` for the device list and can request the
network map. The parse function is pure (testable); the client uses aiomqtt
(optional `host` extra).
"""

from __future__ import annotations

import json

from .registry import DeviceInfo, DeviceRegistry

_Z2M_TYPE = {"Coordinator": "coordinator", "Router": "router", "EndDevice": "end_device"}


def parse_z2m_devices(devices: list[dict]) -> list[DeviceInfo]:
    """Parse a Zigbee2MQTT `bridge/devices` payload into DeviceInfo list."""
    out: list[DeviceInfo] = []
    for d in devices:
        ieee = None
        if d.get("ieee_address"):
            try:
                ieee = int(str(d["ieee_address"]).replace("0x", ""), 16)
            except ValueError:
                ieee = None
        short = d.get("network_address")
        if isinstance(short, str):
            short = int(short, 16)
        out.append(DeviceInfo(
            short=short, ieee=ieee,
            name=d.get("friendly_name"),
            type=_Z2M_TYPE.get(d.get("type")),
            source="z2m"))
    return out


class Z2MClient:
    """Async MQTT client that keeps a DeviceRegistry up to date from Z2M."""

    def __init__(self, registry: DeviceRegistry, host: str, port: int = 1883,
                 base: str = "zigbee2mqtt", username: str | None = None,
                 password: str | None = None):
        self.registry = registry
        self.host, self.port, self.base = host, port, base
        self.username, self.password = username, password

    async def run(self) -> None:
        import aiomqtt  # optional dep (host extra)
        topic = f"{self.base}/bridge/devices"
        async with aiomqtt.Client(self.host, port=self.port, username=self.username,
                                  password=self.password) as client:
            await client.subscribe(topic)
            # ask the bridge to (re)publish the device list + a network map
            await client.publish(f"{self.base}/bridge/request/networkmap",
                                 json.dumps({"type": "raw", "routes": True}))
            async for message in client.messages:
                if str(message.topic) == topic:
                    try:
                        devices = json.loads(message.payload)
                        self.registry.update(parse_z2m_devices(devices))
                    except (ValueError, TypeError):
                        pass
