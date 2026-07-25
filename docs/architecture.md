# Architecture

## The problem this solves

Intermittent Zigbee dropouts on a Home Assistant network: devices periodically go unresponsive
and need a physical power cycle. The coordinator's own tools (ZHA/Z2M maps) show *its* view of
the mesh, not the raw RF reality. This platform captures **what is actually on-air** when a
device drops and correlates it with channel noise and the coordinator's view.

## System overview

```
                 ┌──────────────────── ESP32-C6 (capture core) ─────────────────────┐
                 │  esp_ieee802154: promiscuous RX, RSSI/LQI, channel set, ED scan   │
                 │  shared framing/codec  ──►  one of three transports/builds        │
                 └───────────┬───────────────────────┬───────────────────┬──────────┘
                   usb-sniffer│ (USB serial)  standalone│ (WiFi WS)  satellite│ (SPI)
                              ▼                          ▼                     ▼
                  ┌──── tethered Go host ───┐   ┌── on-device lite UI ──┐   forwards to
                  │ decode + decrypt + DB   │   │ browser: decode +     │   a primary
                  │ incidents + REST/WS API │   │ routing + incidents   │
                  │ embedded web UI         │   │ + spectrum            │
                  │ ZHA names/LQI · export  │   └───────────────────────┘
                  └─────────────────────────┘
        (legacy Python host under host/ keeps the pcap/CLI tools + HA add-on)
```

## Three firmware builds, one capture core

All builds share `firmware/src/core` (radio capture, ED scan, framing codec, NVS config). A
PlatformIO `build_src_filter` selects exactly one build dir on top of the core:

| Build | WiFi | Output | Role |
|-------|------|--------|------|
| `usb-sniffer` | off | framed USB serial | high-fidelity "true sniffer" + host feed |
| `standalone` | on | WiFi HTTP/WS | self-contained diagnostic unit (no PC) |
| `satellite` | off | SPI to primary | extra radio on the carrier board |

Why separate builds and not one binary: the ESP32-C6 has ~320 KB RAM, no PSRAM, and a single
2.4 GHz front end shared between WiFi and 802.15.4. Running high-fidelity capture *and* a WiFi
web server well at the same time isn't realistic, and the two contend for the antenna. Splitting
keeps each build lean. See [hardware.md](hardware.md).

## Decode tier strategy

The capture core stays thin — it ships raw frames + metadata, not decoded Zigbee. Decoding and
optional decryption happen at the **presentation tier** so the heavy logic isn't duplicated in C:

- **`tethered` → Go host:** full MAC/NWK/APS/ZCL decode, AES-CCM* decryption, SQLite history,
  routing/spectrum/networks/incident analytics, embedded web UI. (The `usb-sniffer` build dir and
  `BUILD_USB_SNIFFER` flag are the older name for the `tethered` PlatformIO env.)
- **`standalone` → browser:** the on-device lite UI decodes frames and renders the routing graph
  and spectrum client-side (WebCrypto for AES-CCM*); the firmware only does lightweight
  aggregation + a LittleFS incident log (Block B).

A single [wire framing protocol](../protocol/framing.md) is shared across USB serial, SPI, and
the WebSocket, so one parser serves every transport.

## 802.15.4-first design (Zigbee now, Thread/Matter later)

The capture core, ED spectrum scan, Wireshark export, and wire framing all carry **raw 802.15.4
MPDUs**, agnostic to the upper-layer protocol. Zigbee is simply the first **decode profile**.
Because **Matter-over-Thread uses the same radio**, capture + RF diagnostics already cover it;
adding Matter means a second decode profile, not a new capture path. See
[future-enhancements.md](future-enhancements.md).

## Build phases

See the top-level plan. In short: **Block A** (capture core + USB true-sniffer) → **Block B**
(standalone diagnostic UI — the priority) → **Block C** (full host app + HA integration +
add-on) → **Block D** (optional multi-radio + carrier board).

## Component map

| Concern | Location |
|---------|----------|
| Wire protocol (source of truth) | [`protocol/`](../protocol/) |
| Firmware capture core | `firmware/src/core/` |
| Firmware builds | `firmware/src/{usb-sniffer,standalone,satellite}/` |
| Host app (decode, API, analytics) | `host/zbsniff/` |
| Wireshark/pcap export | `host/zbsniff/store/pcap.py` |
| Full web UI | `frontend/` |
| Carrier board | `hardware/carrier/` |
| HA add-on packaging | `addon/` |
