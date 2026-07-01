# API (REST + WebSocket)

> Status: design. Implemented in Block C (host app) and mirrored by a lighter WS API on the
> standalone device (Block B). Documented here so the frontend and integrations can be built
> against a stable contract.

The host app (FastAPI) serves the React UI and exposes:

## WebSocket `/ws`

Push channel for live data (newline-delimited JSON / binary framing per
[protocol/framing.md](../protocol/framing.md)). Message kinds:
- `frame` — decoded captured frame (addresses, type, RSSI/LQI, decoded summary).
- `ed` — energy-detect sample (channel, dBm, sweep id).
- `status` — per-radio status/counters.
- `incident` — a newly detected incident.
- `device` / `link` — device-registry and link-quality updates.

Client → server: `set_channel`, `set_mode`, `set_hop`, `ed_scan`, `set_key`, `start`, `stop`
(mapped to the device commands).

## REST (read/query history)

| Method | Path | Returns |
|--------|------|---------|
| GET | `/api/devices` | device registry (addr, name, role, last seen, link stats) |
| GET | `/api/devices/{addr}/messages` | recent decoded messages for a device |
| GET | `/api/routing` | nodes + edges for the routing tree (LQI-colored) |
| GET | `/api/spectrum?from=&to=` | ED time-series for the waterfall + channel advice |
| GET | `/api/incidents` | incident list (filterable) |
| GET | `/api/incidents/{id}` | incident detail + context + pcap slice link |
| GET | `/api/export/pcap?from=&to=` | pcap (LINKTYPE_IEEE802_15_4_TAP) of a time range |
| GET/PUT | `/api/config` | settings (channels, key, hop, HA integration, thresholds) |
| GET | `/api/radios` | connected radios + roles |

The exact schemas are defined with Pydantic models in `host/zbsniff/api/` and published as
OpenAPI at `/docs` when the host app runs.

## On-device (standalone build) — implemented

The standalone firmware serves a lighter set directly over WiFi:

| Path | Returns |
|------|---------|
| `GET /` | the embedded gzipped lite UI |
| `GET /ws` | WebSocket: binary `CAPTURED_FRAME` / `ED_RESULT` / `STATUS` / `INCIDENT` frames out; binary command frames in (`CMD_SET_CHANNEL`, `CMD_SET_MODE`, `CMD_ED_SCAN`, `CMD_SET_KEY`) |
| `GET /incidents` | the persisted on-device incident log (JSON lines from `/spiffs/incidents.jsonl`) |

The WS uses the same [binary framing](../protocol/framing.md) as the serial protocol, so the
browser shares one decoder with the firmware and host.
