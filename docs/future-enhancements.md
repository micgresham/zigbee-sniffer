# Future enhancements (roadmap)

Items deliberately out of the initial scope but designed-for. Nothing here is committed; this
records intent and feasibility so the architecture doesn't paint us into a corner.

## Matter / Thread support  ⭐ (requested)

**Feasibility: high for capture + RF diagnostics; partial for app-layer decode.**

Matter's relevant transport is **Thread, which uses the same IEEE 802.15.4 radio** as Zigbee.
The platform is intentionally **802.15.4-first**: the capture core, energy-detect spectrum scan,
Wireshark TAP export, and the wire framing all carry **raw MPDUs**, agnostic to whether the
payload is Zigbee or Thread. So a large part of Matter support already exists:

| Capability | Status for Thread/Matter |
|------------|--------------------------|
| Frame capture (PHY/MAC) | **Works today** — same radio, same capture core, no firmware change |
| RSSI/LQI, retries, timing | **Works today** — protocol-agnostic |
| RF spectrum / ED interference | **Works today** — energy is energy |
| Wireshark dissection | **Works today** — `LINKTYPE_IEEE802_15_4_TAP` already dissects Thread/6LoWPAN |
| Incident logging (silence/retry/route-fail) | Mostly carries over — rules are generic |
| NWK decode | New work — **6LoWPAN / IPv6 / UDP / MLE**, Thread network-layer crypto |
| Routing tree | New work — Thread mesh model (RLOC16, router/REED/child) differs from Zigbee |
| App-layer (Matter cluster) decode | **Limited** — Matter uses per-session keys (PASE/CASE) not obtainable passively |

### What "adding Matter" means concretely
1. A second **decode profile** alongside Zigbee in `host/zbsniff/decode/` (and the browser
   client): 6LoWPAN reassembly → IPv6/UDP → MLE/Thread, with the **Thread network key** for
   network-layer decryption (entered like the Zigbee key — see
   [decoding-and-decryption.md](decoding-and-decryption.md)).
2. A **Thread routing reconstructor** mirroring [routing.md](routing.md) but for the Thread mesh.
3. **OTBR / Thread integration** (analogous to [ha-integration.md](ha-integration.md)) to resolve
   addresses to friendly names and compare against the border router's view.
4. UI: a network-type toggle (Zigbee / Thread) reusing the same device/routing/spectrum/incident
   views.

### Known limits
- Matter application payloads (cluster commands) are end-to-end encrypted with session keys that
  passive sniffing can't recover — expect Thread-level + metadata visibility, not full Matter
  command decode.
- Matter-over-**Wi-Fi** is a different radio and out of scope for this 802.15.4 tool.

## WiFi-backhaul mesh range extender (Zigbee/Matter over WiFi) ⭐ (requested)

**Goal:** extend a Zigbee network's reach the way WiFi mesh extenders extend WiFi — a set of
these nodes form a **WiFi backhaul mesh** and tunnel Zigbee between them, so a device far from the
coordinator can reach it via a chain of WiFi-linked Zigbee routers.

**This is a new product mode, not a sniffer feature** — the device becomes an *active* participant
(it transmits and joins the Zigbee network as a router), which is a different firmware stack from
the passive sniffer. Tracked as a major roadmap item.

### Why a *pair* of C6s per node (the user's instinct is right)
This is the same constraint that drove the carrier design: **one C6 can't run a WiFi mesh and the
802.15.4 radio at once** (shared radio/antenna — see [hardware.md](hardware.md)). So each node is
**two C6s**:
- **Radio-C6** — runs the **Zigbee router** stack (esp-zigbee-sdk / ZBOSS), joined to your network
  as a Zigbee Router (ZR). 802.15.4 only, WiFi off.
- **WiFi-C6** — runs the **WiFi backhaul mesh**, no 802.15.4.
- Linked by the same **SPI/UART + SYNC** interconnect as the carrier ([carrier.md](carrier.md));
  the radio-C6 hands frames to the WiFi-C6 for tunneling and vice-versa.

### WiFi backhaul options
- **ESP-WIFI-MESH / esp-mesh-lite** — Espressif's self-forming/self-healing WiFi mesh; nodes
  auto-discover and relay. Best fit for "automatically mesh with other instances."
- **Infrastructure WiFi + peer discovery** — each node joins your existing WiFi (the "programmed
  WiFi network") and finds peers via mDNS, tunneling over UDP/TCP. Simpler, relies on your APs.
- Either way the Zigbee payload is carried as **encrypted tunnels** between nodes.

### The hard part (honest assessment)
Transparently tunneling Zigbee over an IP/WiFi backhaul so the Zigbee mesh treats it as a normal
link is **non-trivial**:
- Zigbee routing + MAC timing (ACK windows, poll intervals) assume direct RF links; WiFi adds
  latency/jitter that can break timers. Likely needs a **distributed-router / proxy** model rather
  than a literal RF tunnel.
- Requires the **network key** to participate; key rotation handling.
- Loop/route-stability management across the WiFi mesh.
- Real prior art exists for "Zigbee range extender" (RF) and "Zigbee-over-IP gateways", but
  WiFi-backhaul-tunneled Zigbee routers are uncommon — expect meaningful R&D.

### Matter / Thread over the same backhaul
- **Thread** already self-meshes over 802.15.4, so a node's radio-C6 could be a **Thread router**;
  bridging Thread *between* sites over WiFi is essentially distributed **Thread Border Router**
  territory (TBRs normally bridge Thread↔IP infrastructure rather than Thread-mesh-over-WiFi).
- **Matter-over-WiFi** devices already use WiFi directly, so "Matter this way" mostly means the
  Thread side. Feasible but its own large effort; share the WiFi-C6 backhaul + the radio-C6
  running OpenThread instead of Zigbee.

### Reuse from this project
- The **two-C6 + SPI/SYNC** hardware (carrier) and the satellite/primary firmware split.
- The framing/transport code and the build-profile structure (add `zb-router` / `mesh-wifi`
  profiles alongside `usb-sniffer` / `standalone` / `satellite`).
- The decode/decrypt logic (host + Go) for any diagnostics on the extender mesh.

## Active testing / device verification mode

- **MAC-level probe — ✅ IMPLEMENTED (tethered).** Firmware transmits a MAC data frame to a target's
  short address with the ACK-request bit set (`CMD_PROBE` → `PROBE_RESULT`); the target's 802.15.4
  MAC auto-ACKs if alive and on-channel — no key/join needed (the ACK precedes any NWK processing).
  Host: `POST /api/probe` with **per-radio targeting** (`&port=` — dedicate secondary C6s as
  testers), a recurring **scheduler** (`/api/schedules`, "cron"), and **history**
  (`/api/probe_history`, per-device ACK rate). UI: "Active testing" panel + a "Probe now" button in
  the device drawer. Verifies always-on routers/repeaters; sleepy end-devices only ACK while
  polling. PAN id is auto-loaded from the ZHA backup. **Remaining (future):**
- **ZDO/ZCL ping (real verification):** send a ZDO **Node Descriptor** or **Active Endpoints**
  request, or a ZCL **Read Attributes** (e.g. Basic cluster), and decode the reply. This requires
  acting as a **network participant** — a valid short address, the network key, APS counters, and
  correct NWK/APS framing — i.e. effectively joining the network (esp-zigbee-sdk / ZBOSS), not just
  sniffing. Biggest effort, but the most useful ("is this exact device responsive right now?").
- **Round-trip timing / retry stats:** once probing exists, measure response latency and retry
  counts per device — excellent for the dropout diagnosis.

**Scope:** the MAC probe (done) was a moderate add. Full ZDO/ZCL verification overlaps the "Zigbee
router mode" work (joining the network) and is a large feature — staged for later. **Safety:**
active TX participates in your live network — it's gated behind explicit UI actions.

## Other candidate enhancements

- **GPS/PoE-powered remote satellites** for whole-house RF surveys.
- **Long-term trend analytics** (per-device link degradation over weeks; predictive "this device
  is trending toward a drop").
- **Alerting** (push/HA notification when an incident fires).
- **Replay/import** of external pcaps into the analytics pipeline.
- **Sub-GHz / 802.15.4g** would require different hardware (out of scope for the C6's 2.4 GHz radio).
