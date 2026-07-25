# Multi-radio (optional, 0–3 satellites)

> Status: **implemented (Block D)**. Host-mode multi-dongle merge works now (repeated `--port`).
> Standalone SPI aggregation is built: the `satellite` firmware (SPI-slave forward,
> `firmware/src/satellite/transport_spi.c`) and the primary aggregator
> (`firmware/src/standalone/spi_master.c`, runtime-gated by `satellites_enabled`). The carrier
> PCB is a fabrication-ready design package (`hardware/carrier/`) — prototype on perfboard first.
> All on-hardware-pending.

A single ESP32-C6 captures one channel at a time and is briefly deaf while processing a frame.
Adding up to **3 satellite radios** removes both limits. Single-radio remains the default; this
is purely additive.

## Radio roles

1. **Capture + spectrum split** *(the headline use)* — one radio pinned to the HA channel for
   uninterrupted capture while another continuously runs the ED sweep ([spectrum.md](spectrum.md)).
2. **Same-channel diversity** — multiple radios on the HA channel; merge and **dedupe by
   sequence number** so a frame missed by one is caught by another. Improves capture completeness
   for the dropout hunt.
3. **Multi-channel survey** — radios on different channels to watch several at once.

> **Satellites are full radios (firmware ≥ 0.32).** A satellite is no longer capture-only — it
> can take **any** role in the Config → Radios table: `sniffer`, `spectrum` (ED sweep),
> `tester` (active probe TX), or `hopper`. The host wraps each command in `CMD_SAT_RELAY` so it
> reaches the satellite over SPI, and the satellite's ED/probe results are relayed back up
> through the primary. This means a satellite **placed at a problem device's location** can
> measure that spot's noise floor or probe from there — exactly the "survey near the problem
> device" case [spectrum.md](spectrum.md) calls for — while the primary keeps capturing your
> home channel uninterrupted.

## Two ways to connect

### Host/USB mode (no wiring, scales to 3 easily)
Plug each board (running `usb-sniffer`) into the host — directly or via a powered USB hub /
[carrier](carrier.md). The Python host merges all serial streams:
```bash
python -m zbsniff.tools.pcap_bridge --port /dev/ttyACM0 --port /dev/ttyACM1 --port /dev/ttyACM2
```
Recommended path for full 3-satellite fidelity (the host has the CPU/RAM to merge + analyze).

### Standalone SPI mode (no host PC)
Satellites run the `satellite` build (SPI slave) and forward to the primary over a shared SPI
bus + a `SYNC` line. The primary stays **thin** (merge/dedupe + WebSocket forward + LittleFS
incident trigger); decode/routing/analytics run in the **browser**. See [carrier.md](carrier.md)
for wiring and the resource rationale.

## Timestamp alignment

- With the `SYNC` line, the primary pulses all satellites so their timestamps share a reference
  (sub-ms) — needed to correlate retries/relays *between* radios.
- Without it, the primary/host timestamps on arrival — fine for most diagnostics, looser for
  cross-radio timing.

## Dedupe

Same-channel diversity will see the same frame from multiple radios. Dedupe key = (source addr,
MAC sequence number, ~time window); keep the copy with the best RSSI/LQI and record which radios
heard it (a coverage signal in itself).
