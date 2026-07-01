# zbsniff — tethered host (Go)

Single static **Go binary** for tethered (USB) operation: reads framed 802.15.4 captures from
one or more ESP32-C6 sniffer dongles over USB, decodes them (MAC/NWK/APS/ZCL, with optional
**AES-CCM\*** decryption), stores them in SQLite, and serves a web UI + REST/WebSocket API. No
Python, no pip — one executable.

This is the **primary tethered analysis tool**. The Python app under [`../host/`](../host/)
remains as the reference/spec (and the Go decode is cross-checked against it).

## Build

```bash
cd tether
go build -o zbsniff ./cmd/zbsniff      # produces a ~15 MB self-contained binary
```

## Run

Flash a C6 with the **`tethered`** firmware (`pio run -e tethered -t upload`), then:

```bash
./zbsniff --channel 15          # auto-detects the ESP32 native-USB port
# or be explicit:
./zbsniff --port /dev/cu.usbmodemXXXX --channel 15
# open http://localhost:8080
```

- **Auto-detect:** with no `--port`, zbsniff finds the C6's **native USB** device by its Espressif
  USB vendor ID (`303A`) and opens it automatically. It deliberately ignores the UART-bridge port.
- **Port (the #1 gotcha):** the tethered build streams the binary protocol over the C6's **native
  USB** port (`/dev/cu.usbmodem…` / `/dev/ttyACM…` / `COMx`) — i.e. the connector labelled **`USB`**
  on the DevKitC-1. The **`UART`** connector (a CP2102/CH340 bridge, VID `10C4`/`1A86`) only carries
  console logs; pointing zbsniff at it gives "host up — no device data". Plug into the **`USB`**
  port. The web UI's status pill shows **green "receiving from device"** only when real frames flow.
- **Channel:** set to your Home Assistant Zigbee channel (or change it later in the UI).
- **Decryption:** add `--key <32 hex chars>` (your Zigbee network key) to decode NWK/APS/ZCL.
- **Multiple radios:** pass `--port` repeatedly; the host merges the streams.

| Flag | Default | Meaning |
|------|---------|---------|
| `--port` | (none) | serial port; repeatable. Omit to run API/UI only. |
| `--channel` | 0 | capture channel 11–26 to set on start |
| `--key` | "" | Zigbee network key (32 hex chars) for decryption |
| `--db` | `zbsniff.sqlite` | SQLite path |
| `--http-port` | 8080 | web UI / API port |
| `--baud` | 921600 | serial baud (USB-CDC ignores it) |
| `--zha-backup` | "" | ZHA backup JSON → identify devices by IEEE address (offline) |
| `--ha-host` | "" | HA host or IP (URL built as `ws://<host>:8123/api/websocket`) |
| `--ha-token` | "" | HA long-lived access token (with `--ha-host`) |
| `--config` | `zbsniff.yaml` | settings file — auto-saved and reloaded next run |

### Saved configuration

Settings are persisted to **`zbsniff.yaml`** (override with `--config`) and reloaded on the next
run, so you only enter the HA host/token, channel, key, etc. once. Precedence: an **explicit CLI
flag wins**, otherwise the file value is used. The file is rewritten (mode `0600`, it holds the HA
token) whenever you connect HA, change channel, or set the key from the UI.

### Device names

If a device can't be identified from packets, supply either a **ZHA backup** (offline IEEE
mapping) or your **HA API** (friendly names). For HA you only give the **host or IP** — the
WebSocket URL is built for you:
```bash
./zbsniff --zha-backup "ZHA backup ….json"          # IEEE-based labels
./zbsniff --ha-host homeassistant.local --ha-token <token>   # friendly names + Net LQI/RSSI
```

### Network key

Only the **`network_key`** from your ZHA backup is needed for decryption (it decrypts the
NWK/APS payloads). The `tc_link_key` and per-device `key_table` entries are APS link keys, used
only to decrypt join/rekey and a few APS-secured commands — not required for normal traffic, and
a future enhancement. Note: **child devices show up even *without* the key**, because the NWK
header (source/destination addresses) is unencrypted.

## Control & Config (web UI + REST)

The UI has a **Control & Config** panel that drives the node live; every control maps to a REST
endpoint (so it's scriptable too):

| Action | Endpoint |
|--------|----------|
| Start / Stop / Restart capture | `POST /api/start` · `/api/stop` · `/api/start` |
| Reset radio (soft) | `POST /api/reset_radio` |
| Set mode (1=capture, 2=ED sweep, 3=capture+ED, 0=idle) | `POST /api/mode?m=N` |
| Set channel | `POST /api/set_channel?ch=N` |
| Channel hopping (dwell ms/ch, 0=pinned) | `POST /api/hop?dwell=MS` |
| One-shot RF spectrum scan | `POST /api/scan` |
| Set / clear decryption key **live** (no restart) | `POST /api/key?k=<hex>` |
| Save key + channel to device NVS (persists power-cycle) | `POST /api/save_key` |
| Raw command (advanced) | `POST /api/raw?type=<n>&hex=<payload>` |
| Connect Home Assistant (pull ZHA names) | `POST /api/ha_connect?url=&token=` · `GET /api/ha_status` |
| Automatic diagnostics (route failures, weak links/signal, channel noise) | `GET /api/diagnostics` |
| Current config + latest device status | `GET /api/config` · `GET /api/status` |

### Network key — you only need one

A ZHA backup has three kinds of keys. zbsniff **auto-extracts the `network_key`** (and channel)
when you pass `--zha-backup` — that's the only one needed; it decrypts all normal NWK/APS traffic.
The `tc_link_key` (the well-known "ZigbeeAlliance09") and the per-device `key_table` are APS link
keys used only for join/rekey exchanges — not required for normal capture.

### Home Assistant

Connect from the UI's Control panel (just the **host/IP** + long-lived token) or via flags
(`--ha-host homeassistant.local --ha-token <token>`). zbsniff builds the WebSocket URL, pulls the
ZHA device list, and fills friendly names + **Net LQI/RSSI** across the dashboard, graph, and
detail drawer. The host/token are saved to the config file so it reconnects automatically next run.

### Spectrum analyzer

The RF panel does an energy-detect sweep of channels 11–26 with adjustable **dwell**, a
**Continuous** mode, **peak-hold** markers, a **Wi-Fi overlap** row (which Wi-Fi channel sits on
each Zigbee channel), and a **waterfall** (energy over time). `POST /api/scan?dwell=<ms>` /
`POST /api/mode?m=2` (continuous) drive it.

### Routing tree

Radial layout by hop-distance from the coordinator. **Scroll to zoom, drag to pan**, "Reset view"
to recenter, and toggle **End devices** to show only the routing backbone or include leaf clients.

### Signal source: sniffer vs network (important)

Every RSSI/LQI the sniffer shows is **observed at the sniffer's single location** (marked **⌖**),
**not** the device's link to its nearest router. With repeaters present, a device can have a strong
*network* link yet be heard weakly by the sniffer across the room — expected, not a fault. Three
distinct vantage points are surfaced:

- **Sniffer ⌖** — RSSI/quality of frames as heard by the sniffer (depends where the sniffer is).
- **Network** — LQI/RSSI the coordinator reports for the device's real link (from Home Assistant;
  shown as "Net LQI" and in the device drawer).
- **Active probe** — RSSI of an ACK to a probe sent from a *specific* radio's location (below).

"Weak signal" in diagnostics is therefore labelled **⌖ at the sniffer** — move the sniffer (or a
secondary radio) near a suspect device, or check Net LQI, before blaming the mesh.

### Radio roles (multi-dongle)

With up to 4 C6 dongles plugged in (one `--port` each), assign each a **function** in the
**Config → Radios** panel. Roles are keyed by the dongle's **radio-id** (set a unique id per dongle
with the "set" button — persisted to its NVS) and saved to the config file, so they re-apply on
reconnect/restart:

| Role | What the host configures |
|------|--------------------------|
| **any** | *default* — unassigned; the system may task it to any function on demand |
| **sniffer** | capture, pinned to the channel (passive) |
| **spectrum** | continuous energy-detect sweep — feeds the RF spectrum tab without pausing capture |
| **tester** | capture + becomes the **default radio for active probes/TX** |
| **hopper** | capture with channel-hopping (multi-channel survey) |
| **idle** | radio parked |

The host applies the role's commands to whichever port currently carries that radio-id
(`POST /api/radio_role?radio=<id>&role=<role>`, `POST /api/set_radio_id?port=&id=`).

**Dynamic allocation.** When a function is requested (capture, spectrum sweep, or probe) the host
picks a radio: first one **dedicated** to that role, else a free **any** radio, else (for probes) a
capturing radio to piggyback on. If the only candidate is mid-task it **asks to confirm** (a single
C6 can't capture and sweep at once); if nothing is free it **refuses and reports what each radio is
doing**. `GET /api/allocate?fn=capture|spectrum|probe` is the dry-run the UI uses to gate buttons.
So with one radio left as **any**, the app time-shares it with warnings; add a second radio and set
one to **spectrum**/**tester** and the contention disappears — no prompts.

### Active testing (verify a device is alive)

`POST /api/probe?addr=0x1234[&port=…]` transmits a MAC data frame to the device with the ACK bit
set; an ACK proves its radio is **alive and on-channel right now** (works for always-on
routers/repeaters; sleepy end-devices only ACK while polling). No network key or join required —
the ACK is a MAC-layer reply. Needs the **PAN id** (auto-loaded from the ZHA backup).

- **Per-radio targeting:** with multiple dongles, `&port=` aims a *specific* radio — dedicate
  secondary C6s as active testers at different locations while the primary keeps sniffing.
  `GET /api/radios` lists connected radios.
- **Scheduler ("cron"):** `POST /api/schedules?addr=&interval=&port=` runs a recurring probe to
  watch a device's responsiveness over time — the per-device ACK rate is the dropout signal.
  History: `GET /api/probe_history[?addr=]`.

> ⚠ Active mode **transmits on your live network**; it's gated behind explicit UI actions.

### Link quality (LQI vs RSSI)

The ESP32-C6's 802.15.4 **LQI reads unusually low** (single digits even with a strong −49 dBm
signal) — it isn't the standard 0–255 perceived-quality scale. The UI therefore derives a **link
quality %** from **RSSI** (−45 dBm → 100 %, −95 dBm → 0 %), uses it to color the routing edges and
device list, and keeps the raw LQI alongside for reference.

### Export

Buttons in the Control panel (and `GET /api/export?what=<devices|frames|incidents|diagnostics|
spectrum>&format=<csv|json>`) download the collected data.

### Diagnostics window

The **Connection & logs** panel shows the live serial link (port, bytes, frames, last-message age)
plus the host log — so a "no device data" condition is self-explanatory (0 bytes = wrong/​busy
port; bytes but no frames = firmware mismatch). The host also **self-heals**: it retries a busy
port every 3 s.

### Automatic diagnostics

The **Diagnostics** panel continuously flags issues from the live capture:
- **Route failures** — decoded NWK "Network Status" frames naming the unreachable device + reason
  (the strongest signal for "device went unresponsive").
- **Weak links / weak signal** — links with LQI < 50, devices with RSSI < −85 dBm.
- **Channel noise** — busy channels from the energy-detect scan, with a quieter-channel suggestion.

The panel also shows a **device status** readout (mode, radio id, firmware, uptime, hop, captured,
dropped) from the device's periodic STATUS frames.

## Layout

```
cmd/zbsniff/       main: flags, wiring, ingest loop
internal/proto/    wire framing (+ tests)
internal/decode/   MAC/NWK/APS/ZCL + AES-CCM* (ccm.go) (+ tests, cross-checked vs Python)
internal/store/    SQLite (pure-Go modernc driver)
internal/serialio/ serial reader (go.bug.st/serial)
internal/api/      HTTP + WebSocket (coder/websocket)
internal/webui/    embedded single-page UI (//go:embed)
```

## Test

```bash
go test ./...
```
Covers CRC, framing roundtrip, MAC decode, and an **AES-CCM\* decrypt roundtrip** validated
against a vector produced by the (separately-tested) Python implementation.
