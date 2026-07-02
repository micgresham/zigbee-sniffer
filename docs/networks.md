# Seeing other Zigbee networks

> Status: implemented. Networks panel + passive survey in the host UI (`internal/webui`);
> active (beacon-request) scan needs firmware ≥ 0.26 (`core/beacon.c`, `CMD_BEACON_REQ`).

Your own network is not the only thing on the air. Neighbouring Zigbee coordinators
(other Hubs, Hue bridges, IKEA gateways) and Thread/Matter border routers all share the
2.4 GHz 802.15.4 band, and one of them sitting on **your** channel is a classic cause of
the intermittent dropouts this tool exists to diagnose. The **Networks** panel (RF spectrum
tab) shows every network the sniffer has heard.

## What identifies a network

Every 802.15.4 frame carries a **PAN id** (a 16-bit network identifier). The host records
each PAN it sees in the `pans` table (`store/db.go`) — for normal traffic that's the frame's
*destination* PAN; for beacon replies it's the *source* PAN. The Networks panel lists them:

| Column | Meaning |
|--------|---------|
| PAN | the network id, e.g. `0xe092`. Tagged **yours** (matches your coordinator's PAN, resolved from Home Assistant) or **foreign**. |
| Channel | the channel it was heard on. |
| Frames | how many frames from that network the sniffer has decoded. |
| Last seen | age of the most recent frame. |
| Identity | the network's manufacturer or device name — see below. |

## Naming the networks (Identity)

Foreign PANs start as bare hex ids, but the host puts a name to them:

- **By manufacturer (OUI), no setup.** Every extended (IEEE) address begins with a 24-bit OUI that
  identifies the vendor. When the sniffer hears an extended address on a PAN, it labels that network
  — **Philips Hue** (`00:17:88`), **Aqara/Xiaomi**, **IKEA/Silicon Labs**, **Texas Instruments**, … —
  automatically. Table: [`tether/internal/names/oui.go`](../tether/internal/names/oui.go).
- **By integration.** Pairing a **Philips Hue bridge** (Config → Philips Hue bridge) adds real
  *device* names on top, and confirms the Hue network's channel. See
  [ha-integration.md](ha-integration.md). Home Assistant/ZHA names your own network the same way.

A **foreign PAN on your channel** is the one to worry about — it's another network
contending for the same airtime. Foreign PANs are also raised in
[Automatic diagnostics](incidents.md) as a `notice`.

## The limit: one radio hears one channel

A single ESP32-C6 listens to **one channel at a time**. So a normal capture only ever sees
networks on *your* channel. To find networks elsewhere you have to visit the other channels —
that's what a **survey** does. (With [satellite radios](multi-radio.md) one radio can survey
while another keeps capturing.)

## Passive survey

**Passive survey** hops channels 11→26, capturing for ~1.5 s on each. Any network actively
transmitting during its window is recorded with its channel. It's non-transmitting and safe,
but a *quiet* network that happens not to send anything in its 1.5 s slot is missed.

- Host: `POST /api/survey` (optionally `?dwell=<ms>`). Per-channel progress streams on `/ws`
  as `{"kind":"survey","channel":N,"active":false,"done":false}`.
- On a single radio the survey **pauses your capture** for the ~30 s sweep, then restores your
  original channel automatically.

## Active scan (beacon request)

**Active scan** is the reliable way to discover *idle* networks. On each channel the sniffer
transmits an 802.15.4 **beacon request** — the same broadcast MAC command a joining device
uses to find networks. Every coordinator and router in range answers with a **beacon** whose
MAC header reveals its PAN. Because we solicit a reply instead of waiting for incidental
traffic, even a silent neighbour shows up.

- Firmware: `CMD_BEACON_REQ` (`0x8F`) → `core/beacon.c` wakes the radio and TX's the request
  (`FCF 0x0803`, dst PAN/addr `0xFFFF`, command id `0x07`). Replies arrive through the normal
  capture path and are decoded like any other frame; the host records their source PAN.
- Host: `POST /api/survey?active=1`. Per channel it sets the channel, sends two beacon requests
  (a single one can be lost on a busy channel), then dwells to collect replies.
- This **transmits on-air**. It is exactly what a standard Zigbee network scan does — a
  broadcast MAC command, no network join, no key — but it is a transmission, so it's gated
  behind an explicit button and a confirmation, like the active device probe.
- Needs firmware **≥ 0.26**. Against older firmware the beacon command is ignored and the scan
  degrades to a passive survey.

## Using it

1. Open the **RF spectrum** tab → **Zigbee networks** panel.
2. **Passive survey** to map networks that are currently talking, or **Active scan ⚡** to also
   flush out idle ones.
3. Watch the progress line ("beaconing channel 17…") as the table fills. Your PAN is tagged
   *yours*; everything else is a neighbour.
4. If a foreign PAN shows up **on your channel**, consider moving your network to a quieter
   channel (see the [channel advisor](spectrum.md) and [channels reference](channels-reference.md)).

## Related

- [spectrum.md](spectrum.md) — energy per channel (the Networks panel lives on the same tab).
- [incidents.md](incidents.md) — foreign-network diagnostics.
- [multi-radio.md](multi-radio.md) — survey without pausing capture.
- [protocol/framing.md](../protocol/framing.md) — `CMD_BEACON_REQ` wire format.
