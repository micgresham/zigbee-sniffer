# API (REST + WebSocket)

> Status: implemented in the tethered Go host ([`tether/internal/api`](../tether/internal/api)),
> which serves the embedded web UI. A lighter WS API on the standalone device mirrors the live
> feed. See [host.md](host.md).

## WebSocket `/ws`

Push channel for live data (JSON events). Message kinds (`kind` field):
- `frame` — decoded captured frame (channel, RSSI/LQI, type, decoded summary, decrypted).
- `ed` — energy-detect sample (channel, dBm, sweep id).
- `status` — per-radio status/counters (mode, channel, captured, uptime, fw…).
- `incident` — a newly detected incident (silence / recovered) with channel + ED.
- `survey` — whole-band survey progress (`channel`, `active`, `done`).
- `probe` — active-test probe result (target, acked, RSSI/LQI).
- `ota` — firmware-update progress (target, state, received/total).

Commands go over REST (below), not the socket. The standalone device's WS additionally accepts
binary command frames (`set_channel`, `set_mode`, `ed_scan`, `set_key`).

## REST (read/query history)

| Method | Path | Returns |
|--------|------|---------|
| GET | `/api/devices` | device registry (addr, name, role, last seen, link stats) |
| GET | `/api/devices/{addr}/messages` | recent decoded messages for a device |
| GET | `/api/routing` | nodes + edges for the routing tree (LQI-colored) |
| GET | `/api/spectrum?from=&to=` | ED time-series for the waterfall + channel advice |
| GET | `/api/networks` | every PAN id seen on-air (`networks[]`: `pan`, `channel`, `count`, `last_seen`, `label`) plus `ours` (your PAN) — see [networks.md](networks.md) |
| GET | `/api/decode?hex=` | layer-by-layer decode of one frame (MAC/NWK/APS/ZCL + raw) for the inspector |
| POST | `/api/survey?active=&dwell=` | run a whole-band survey: hop channels 11–26 collecting PANs. `active=1` also TX's a beacon request per channel (needs firmware ≥ 0.26); `dwell` = ms/channel. Progress streams on `/ws` as `{"kind":"survey",...}` |
| POST | `/api/monitor?channel=&dwell=` | watch a foreign network: default channel = paired Hue bridge, else busiest foreign PAN. Uses a spare/satellite radio continuously (`monitor:<ch>` role) if available, else a timed snapshot on the primary (`{"kind":"monitor",...}` on `/ws`) |
| GET | `/api/incidents` | incident list (silence/recovery, newest first) — see [incidents.md](incidents.md) |
| GET/POST | `/api/incident_config?silence=N` | get/set the silence threshold (seconds) the host detector uses |
| GET | `/api/incidents/{id}` | incident detail + context + pcap slice link |
| GET | `/api/export/pcap?from=&to=` | pcap (LINKTYPE_IEEE802_15_4_TAP) of a time range |
| GET/PUT | `/api/config` | settings (channels, key, hop, HA integration, thresholds) |
| GET | `/api/radios` | connected radios + roles |
| POST | `/api/ha_connect?host=&token=` · GET `/api/ha_status` | Home Assistant / ZHA name integration |
| POST | `/api/hue_pair?host=` · GET `/api/hue_status` · POST `/api/hue_forget` | Philips Hue bridge (link-button pairing, then device names) |

Endpoints are registered in [`tether/internal/api/server.go`](../tether/internal/api/server.go);
the JSON responses are plain maps (no formal schema). Additional live endpoints exist there
(`/api/prefs`, `/api/mode`, `/api/hop`, `/api/set_channel`, `/api/key`, `/api/reconnect`,
`/api/reset_radio`, `/api/allocate`, `/api/scan`, OTA, active-testing) — read the file for the
full list.

## On-device (standalone build) — implemented

The standalone firmware serves a lighter set directly over WiFi:

| Path | Returns |
|------|---------|
| `GET /` | the embedded gzipped lite UI |
| `GET /ws` | WebSocket: binary `CAPTURED_FRAME` / `ED_RESULT` / `STATUS` / `INCIDENT` frames out; binary command frames in (`CMD_SET_CHANNEL`, `CMD_SET_MODE`, `CMD_ED_SCAN`, `CMD_SET_KEY`) |
| `GET /incidents` | the persisted on-device incident log (JSON lines from `/spiffs/incidents.jsonl`) |

The WS uses the same [binary framing](../protocol/framing.md) as the serial protocol, so the
browser shares one decoder with the firmware and host.
