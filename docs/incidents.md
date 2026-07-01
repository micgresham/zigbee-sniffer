# Incident logging — the core diagnostic

> Status: **on-device engine implemented (Block B)** — silence detection in firmware
> (`standalone/incidents.c`), persisted to flash (SPIFFS), served at `GET /incidents`, and pushed
> live to the browser via `MSG_INCIDENT`. Additional rules (route errors, rejoin/retry storms)
> and the full host engine with unlimited history + coordinator correlation come in Block C.

## On-device engine (standalone build) — implemented

The firmware keeps a small device table (short address → last-seen, count, RSSI/LQI) updated from
a minimal MAC parse of every frame. Every ~5 s it checks for devices that were seen regularly
(≥ a few frames) and have gone silent past the threshold (default **120 s**, configurable). When
one trips it:
1. snapshots the current **channel energy** (an ED measurement) to test the interference theory,
2. appends a JSON line to `/spiffs/incidents.jsonl` (rotated past ~48 KB) — so dropouts are logged
   **even with no browser or host connected**, which is the whole point for an unattended hunt,
3. pushes the incident to any connected browser via `MSG_INCIDENT`.
A frame from a previously-silent device emits a `recovered` incident. The browser also fetches the
full log from `GET /incidents` on connect.

Incident JSON: `{"ts","addr","reason","rssi","lqi","ch","ed","silent_s"}` (see
[protocol/framing.md](../protocol/framing.md) `0x06 INCIDENT`).

This is the feature aimed squarely at your problem: **devices going unresponsive until power
cycled.** It detects, timestamps, and stores those events with the surrounding RF context so you
can see *what preceded the drop*.

## Detection rules

| Rule | Signal | Likely meaning |
|------|--------|----------------|
| **Silence** | no frames from a device for > threshold (per device-type) | device fell off / hung |
| **Route errors** | NWK status "route failure" / "no route" bursts toward a device | mesh path broke |
| **Rejoin storm** | repeated NWK rejoin / association attempts | device flapping |
| **Retry/ACK spikes** | many MAC retransmits / missing ACKs to a device | marginal link |
| **Checkin gaps** | sleepy end-device polls stop arriving | end-device stalled |
| **Parent change** | device re-parents repeatedly | unstable backbone router |

Thresholds are configurable (a chatty plug vs. a once-a-day sensor differ widely).

## Context captured per incident

- Timestamp + device (address, friendly name if available).
- Rolling **RSSI/LQI trend** for that device over the preceding window.
- **Channel energy (ED)** at the moment — was there an interference spike? (see
  [spectrum.md](spectrum.md)).
- Recent decoded frames to/from the device (the lead-up).
- If [HA integration](ha-integration.md) is on: the coordinator's **availability** state for the
  device, to confirm HA also saw it drop and when it recovered.

## Storage & export

- **On-device (standalone):** a bounded rolling log in the LittleFS `storage` partition — enough
  for recent incidents; oldest rotate out.
- **Host:** unlimited history in SQLite; export an incident timeline (CSV/JSON) and the raw
  pcap slice around each event.

## Using it

Leave a sniffer pinned to your HA channel near the trouble area (a satellite placed by the
problem device is ideal). When a device next hangs, open the incident and read the lead-up:
rising retries + an ED spike points to interference; clean RF + sudden silence points to a
device/firmware fault; route-failure bursts point to a flaky router upstream.
