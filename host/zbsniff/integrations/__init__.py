"""Optional Home Assistant coordinator integrations (Zigbee2MQTT / ZHA).

Cross-references the sniffer's raw RF truth with the coordinator's own view:
resolves sniffed addresses → friendly names and exposes the coordinator's
device list. Entirely optional; the tool works without it.

The parsing is pure/testable; the network clients (aiomqtt / websockets) are
import-guarded so the base install doesn't need them.
"""

from .registry import DeviceInfo, DeviceRegistry
from .z2m_mqtt import parse_z2m_devices
from .zha_ws import parse_zha_devices

__all__ = ["DeviceInfo", "DeviceRegistry", "parse_z2m_devices", "parse_zha_devices"]
