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
  identifies the vendor. The sniffer picks up extended addresses from the MAC header *and* from the
  **unencrypted NWK header's source/dest IEEE fields** (so it works even on foreign networks it
  can't decrypt), and labels that PAN — **Philips Hue** (`00:17:88`), **Aqara/Xiaomi**,
  **IKEA/Silicon Labs**, **Texas Instruments**, … — automatically. A network with few frames may
  stay unlabeled until one of its frames carries an IEEE address. Table:
  [`tether/internal/names/oui.go`](../tether/internal/names/oui.go).
- **By integration.** Pairing a **Philips Hue bridge** (Config → Philips Hue bridge) reports the
  bridge's Zigbee **channel**, so a foreign network on that channel is labeled **"Philips Hue
  (bridge)"** even if no Hue device's address was caught. See [ha-integration.md](ha-integration.md).
  Home Assistant/ZHA names your own network the same way.

- **By protocol shape — Thread/Matter, no setup.** Thread (what nearly all Matter devices run over)
  shares the same 802.15.4 MAC layer as Zigbee but uses 6LoWPAN/IPv6 framing above it instead of
  Zigbee's NWK layer. A payload that fails the Zigbee NWK version check is checked against known
  6LoWPAN dispatch patterns (RFC 4944/6282 — IPHC, uncompressed IPv6, mesh headers, fragmentation);
  a match labels that PAN **"Thread network (likely Matter)"**. This can't confirm the traffic is
  specifically Matter (vs. plain Thread/OpenThread) without that network's key — the same limit as
  any other foreign, encrypted network — but the MAC layer alone is enough to tell it apart from
  Zigbee. See [`tether/internal/decode/layers.go`](../tether/internal/decode/layers.go)
  (`isSixLowPANDispatch`).

> **Single-radio caveat for Hue *device* names:** a Hue bridge usually runs on a *different channel*
> than your ZHA network. With one radio pinned to your channel you can identify the Hue *network*
> (above) but won't hear individual Hue devices, so their names won't appear in Devices. Use
> **Monitor** (below) to snapshot that channel and pick them up.

## Monitoring a foreign network (Hue-prioritised)

The **Monitor** button (and the per-row *monitor* link) on the Zigbee-networks panel watches another
network so its devices get discovered and named. It picks the target channel automatically —
**the paired Hue bridge's channel first**, else the busiest foreign network — and adapts to your
hardware (`POST /api/monitor`):

- **Spare/satellite radio available** → that radio is dedicated to the foreign channel *continuously*
  (a `monitor:<ch>` role, pinned + capturing), while your primary keeps watching your network. This
  is the right way to watch another network long-term; see [multi-radio.md](multi-radio.md).
- **Single radio** → a timed **snapshot**: the radio pins to the target channel for ~20 s (collecting
  that network's devices and names — e.g. Hue device names via the bridge), then restores your
  channel and hop state.

Because a Hue network is encrypted, individual Hue *device* names come from matching the addresses
heard on-air to the bridge's device list (by IEEE/OUI); routers and chatty devices resolve first.

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

## Channel airtime — how loud is each network?

The networks table says *who* is out there; the **Channel airtime** panel below it says **how much
air they use and how loudly**. It rolls every captured frame into a per-minute count per
(PAN, channel), plus an 8-bin RSSI histogram per PAN, kept for 48 h:

- **frames/min by network** over 15m / 1h / 6h / 24h, stacked so the busiest PANs are obvious;
- **per-channel totals** for the window;
- **avg RSSI + histogram per PAN** — the "loudness" axis. High frame rate *and* an RSSI
  distribution piled at the strong end means a busy transmitter physically close to the sniffer.

Why this matters: a strong transmitter near your coordinator degrades its radio **regardless of
channel** (front-end blocking) — e.g. a Hue bridge on channel 25 sitting next to a coordinator on
channel 20 can cause network-wide `NWK_NO_ROUTE` failures and blocked pairing while looking
"innocent" in a channel-only view. The airtime panel shows that neighbour as a fat colour band
with a loud histogram.

Data comes from whatever the radios capture, so the heard-at-sniffer caveat applies: a foreign
network on another channel accumulates airtime only while some radio listens there (hopping,
surveys, or a dedicated monitor/satellite radio). API: `GET /api/airtime?window=<seconds>`.

## Using it

1. Open the **RF spectrum** tab → **Zigbee networks** panel.
2. **Passive survey** to map networks that are currently talking, or **Active scan ⚡** to also
   flush out idle ones.
3. Watch the progress line ("beaconing channel 17…") as the table fills. Your PAN is tagged
   *yours*; everything else is a neighbour.
4. If a foreign PAN shows up **on your channel**, consider moving your network to a quieter
   channel (see the [channel advisor](spectrum.md) and [channels reference](channels-reference.md)).
5. Check **Channel airtime** for any foreign PAN that is both busy and loud (strong-end RSSI
   histogram) — if your sniffer sits near the coordinator, that's a physical-proximity
   interference suspect even when it's on a *different* channel.

## Related

- [spectrum.md](spectrum.md) — energy per channel (the Networks panel lives on the same tab).
- [incidents.md](incidents.md) — foreign-network diagnostics.
- [multi-radio.md](multi-radio.md) — survey without pausing capture.
- [protocol/framing.md](../protocol/framing.md) — `CMD_BEACON_REQ` wire format.
