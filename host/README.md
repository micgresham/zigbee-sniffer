# zbsniff — host app

Python host application for the Zigbee Sniffer & Diagnostics Platform: serial
ingest, frame decode + optional AES-CCM* decryption, SQLite storage, REST/WebSocket
API, Wireshark/pcap export, and (optional) Zigbee2MQTT / ZHA integration.

See [../docs/host.md](../docs/host.md) and [../docs/api.md](../docs/api.md).

```bash
python3 -m venv .venv && . .venv/bin/activate
pip install -e .          # CLI tools (pyserial)
pip install -e '.[host]'  # full host app (fastapi, scapy, cryptography, ...)
pip install -e '.[dev]'   # tests
pytest -q
```
