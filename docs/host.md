# Host app

Python package under [`host/zbsniff/`](../host/zbsniff/). Block A ships the transport + pcap
export + CLI tools; Blocks C add decode/decrypt, SQLite, analytics, the REST/WS API, and HA
integration.

## Install

```bash
cd host
python3 -m venv .venv && . .venv/bin/activate
pip install -e .          # base (pyserial) — enough for the CLI tools
pip install -e '.[host]'  # full host app deps (fastapi, scapy, cryptography, mqtt, ws)
pip install -e '.[dev]'   # pytest, ruff
```

## Modules

| Module | Status | Purpose |
|--------|--------|---------|
| `proto.py` | done | wire framing mirror (encode/decode, command builders) |
| `transport/serial_reader.py` | done | read + merge N serial streams (multi-dongle / hub) |
| `store/pcap.py` | done | LINKTYPE_IEEE802_15_4_TAP pcap writer |
| `tools/listen.py` | done | console monitor |
| `tools/pcap_bridge.py` | done | serial → pcap/Wireshark bridge |
| `decode/{mac,nwk,aps,zcl}.py` | done | MAC/NWK/APS/ZCL decode + `decode_frame()` |
| `decode/crypto.py` | done | AES-CCM* decryption with the network key |
| `store/db.py` | done | SQLite history + device/link aggregation (analytics on ingest) |
| `api/app.py` + `pipeline.py` + `server.py` | done | FastAPI REST + WebSocket + serial→decode→store ingest |
| `integrations/` | pending | Zigbee2MQTT (MQTT) + ZHA (HA WebSocket) enrichment |

The host backend is verified by the test suite (decode, AES-CCM* decrypt roundtrip, SQLite
aggregation, ingest pipeline, REST endpoints). Run it:

```bash
python -m zbsniff.api.server --port /dev/ttyACM0 --channel 15 --db zbsniff.sqlite
# add --key <32 hex chars> to decrypt; open http://localhost:8080
```

## CLI tools

```bash
python -m zbsniff.tools.listen      --port /dev/ttyACM0 --channel 15
python -m zbsniff.tools.pcap_bridge --port /dev/ttyACM0 --channel 15 --out cap.pcap
```
Both accept repeated `--port` for multiple radios. Installed as `zbsniff-listen` and
`zbsniff-bridge` console scripts.

## Tests

```bash
cd host && . .venv/bin/activate && python -m pytest -q
```
Covers CRC vectors, framing roundtrip, the streaming decoder under split/garbage input, and the
pcap/TAP output.
