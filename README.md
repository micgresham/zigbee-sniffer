# Zigbee Sniffer & Diagnostics Platform

A full-featured IEEE 802.15.4 / Zigbee diagnostic tool built on the **ESP32-C6**. Unlike the
existing ESP32 sniffers that only stream frames to Wireshark, this project adds device
discovery, per-device message history, an LQI/RSSI **routing tree**, **incident logging** for
"device went unresponsive" problems, **RF spectrum / interference analysis** using the
radio's Energy-Detect (ED) scan, and **neighbouring-network discovery** (passive survey +
active beacon-request scan across channels 11–26).

> **Why this exists:** to diagnose intermittent Zigbee dropouts on a Home Assistant network —
> capturing what is *actually happening on-air* when a device drops, and correlating it with
> channel noise and the coordinator's own view.

---

## Deployment modes

The firmware ships as **three PlatformIO build profiles** sharing one capture core:

| Build | Runs on | Purpose |
|-------|---------|---------|
| **`standalone`** ⭐ | ESP32-C6-DevKitC-1 | Self-contained: WiFi web UI + on-device analytics + incident log to flash. **No host PC.** Highest-priority deliverable. |
| **`tethered`** | any ESP32-C6 | Lean, WiFi off, high-fidelity. Framed serial → Wireshark **and** the Python host app. The "true sniffer." |
| **`satellite`** | ESP32-C6 SuperMini | Dumb capture-and-forward over SPI to a primary (carrier board, standalone multi-radio). |

A **single-binary Go host** ([`tether/`](tether/)) plugs a USB C6 dongle into your PC/HA box and
serves the whole diagnostic web UI (embedded, no runtime deps): live decode + optional decryption,
SQLite history, routing tree, RF spectrum, neighbouring-network discovery, **automatic
silence/recovery incident logging**, ZHA name/LQI correlation, and CSV/JSON export. (A legacy
**Python / FastAPI** host under [`host/`](host/) still provides the pcap/CLI tools and the HA
add-on payload.) The host self-heals the serial link and encrypts secrets (network key, HA token)
at rest.

See [docs/architecture.md](docs/architecture.md) for the full picture.

---

## Hardware

- **Primary:** ESP32-C6-DevKitC-1 (8 MB flash) — runs `standalone`.
- **Optional satellites (0–3):** ESP32-C6 SuperMini (ESP32-C6FH4, 4 MB) — extra radios for
  capture+spectrum split, same-channel diversity, or multi-channel survey.
- **Optional carrier board:** USB-hub carrier (host mode) and/or standalone SPI-aggregator
  carrier. See [docs/carrier.md](docs/carrier.md).

Full bill of materials and antenna/coexistence notes: [docs/hardware.md](docs/hardware.md).

---

## Repository layout

```
firmware/   PlatformIO ESP-IDF project (standalone / tethered / satellite)
tether/     Go single-binary tethered host (USB radio dongle -> web UI)
host/       Python / FastAPI host app (also the HA add-on payload)
frontend/   React + Vite full UI
protocol/   Shared wire-framing spec (source of truth for all transports)
hardware/   KiCad carrier board
addon/      Home Assistant add-on packaging
docs/       Documentation — "document everything"
```

---

## Quick start

> ### Architecture update (post hardware bring-up)
> The C6's WiFi and 802.15.4 radios share one antenna, so running on-device capture *and* a WiFi
> AP starves WiFi (clients can't even associate). Resolved by a clear rule: **the C6 only runs its
> 802.15.4 radio as a "radio feeder"** —
> - **`tethered`** (USB → host) and **`satellite`** (SPI → primary): radio ON, WiFi off.
> - **`standalone`** (WiFi web UI primary): its own radio **OFF**; it **requires ≥1 SPI satellite**
>   for capture (so WiFi and 802.15.4 never fight).
>
> Two analysis front-ends consume the feed: the on-device standalone UI, and a **tethered Go
> binary** ([`tether/`](tether/)) that plugs a USB dongle into your PC/HA box and serves the web UI
> — single static executable, no Python.

> Build status: **all blocks A–D implemented**; hardware bring-up in progress (radio capture
> confirmed working on the C6).
> - ✅ Firmware — `tethered` (215 KB), `standalone` (WiFi UI + device list + spectrum + routing
>   tree + on-device incident logging), `satellite` (SPI forward). All three build.
> - ✅ Host backend — framing, pcap, MAC/NWK/APS/ZCL decode, **AES-CCM\* decryption**, SQLite,
>   FastAPI REST + WebSocket, ingest pipeline, **Z2M/ZHA name resolution**. **31 tests pass.**
> - ✅ **HA add-on** + docker-compose; React frontend (dashboard) scaffold.
> - ✅ Block D — satellite SPI firmware + primary aggregator; **carrier design package**
>   (`hardware/carrier/`: DESIGN, netlist, BOM, pinout).
> - ⏳ Remaining (need your hw/tools): on-hardware bring-up, KiCad binary layout from the spec,
>   `npm run build` of the frontend, richer per-feature UI pages, live Z2M/ZHA validation.
>
> See [docs/architecture.md](docs/architecture.md) and `docs/` for the full picture.

### Firmware (USB true-sniffer)
```bash
cd firmware
pio run -e tethered -t upload        # flash a C6 board
```
Then stream to Wireshark:
```bash
cd host
pip install -e .
python -m zbsniff.tools.pcap_bridge --port /dev/ttyACM0 | wireshark -k -i -
```

### Firmware (standalone, no PC)
```bash
cd firmware
pio run -e standalone -t upload
# join the "zb-sniffer" WiFi AP (or configure STA) and browse to http://192.168.4.1
```

See [docs/getting-started.md](docs/getting-started.md) for the full walkthrough.

---

## Documentation

**New here? → [Quickstart](docs/quickstart.md) · [User Guide](docs/user-guide.md)** (with diagrams).
Also available in **Word**: [quickstart.docx](docs/quickstart.docx) · [user-guide.docx](docs/user-guide.docx).

![Architecture](docs/images/architecture.svg)

Everything else is under [`docs/`](docs/): [architecture.md](docs/architecture.md),
[networks.md](docs/networks.md) (seeing other Zigbee networks), [spectrum.md](docs/spectrum.md),
[deployment-addon.md](docs/deployment-addon.md) (run inside Home Assistant), [ota.md](docs/ota.md)
(firmware updates), [hardware.md](docs/hardware.md), [troubleshooting.md](docs/troubleshooting.md),
and the [protocol spec](protocol/framing.md).

## Safety & scope

A **diagnostic tool** for *your own* network. It's a passive receiver by default; decryption
requires *your own* Zigbee network key (see
[docs/decoding-and-decryption.md](docs/decoding-and-decryption.md)). The optional **Active testing**
mode transmits a MAC probe (and awaits an ACK) to verify a device is alive — it never joins the
network, and it's gated behind explicit user actions.

## License

MIT (see `LICENSE`).
