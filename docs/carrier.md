# Carrier board (optional)

> Status: **design package complete** (`hardware/carrier/`: DESIGN.md, netlist.csv, bom.csv,
> pinout.md) and the **SPI firmware is implemented** (satellite + primary aggregator). KiCad
> binaries are drawn from the spec. Prototype on an off-the-shelf powered USB hub (host mode) and
> perfboard + jumpers (standalone SPI) first.

Hosts **1× ESP32-C6-DevKitC-1 primary + up to 3× ESP32-C6 SuperMini satellites** and supports
both connection modes on one board.

## Mode 1 — USB-hub carrier (host mode)

- Onboard **4-port USB-2.0 hub IC** (e.g. FE1.1s) with one **USB-C uplink** to the HA/host box.
- Four downstream ports → each board's native USB. Each enumerates as its own serial port; the
  host merges them. **Firmware unchanged** (`usb-sniffer` on every board).
- Lowest-risk path for full 4-radio fidelity. Needs the host running (which you already deploy
  as the HA add-on).

## Mode 2 — standalone SPI-aggregator (no host)

Primary = SPI master; the 3 satellites = SPI slaves on a shared bus.

| Net | Width | Notes |
|-----|-------|-------|
| `SCK` / `MOSI` / `MISO` | 3 shared | SPI bus |
| `CS0..CS2` | 3 | one chip-select per satellite |
| `DATA_READY` | 1 | shared open-drain IRQ: a satellite signals "I have a frame" |
| `SYNC` | 1 | primary → all, timestamp-alignment pulse |

≈ **8 GPIO** on the primary — within the DevKitC-1 budget (avoid strapping/USB/flash pins; final
pin map in the KiCad schematic).

### Why thin firmware here
One C6 running WiFi UI + aggregating 3 radios is near the chip's limit. The primary firmware does
only **merge/dedupe + WebSocket forward + a lightweight incident trigger**; the heavy decode /
routing / spectrum / incident analytics run in the **browser client**, which has the CPU/RAM the
C6 lacks. Bounded ring buffers + drop counters provide backpressure. If a very busy network still
saturates it, fall back to Mode 1 (host mode).

## Power

- Single 5 V source feeds all boards' 5 V pins (each board's onboard LDO makes 3V3); common GND.
- Budget ~1 A (RX-only satellites draw ~0.1–0.2 A each); a 5 V/2 A USB-C supply is ample.
- **Power-source select** (host-USB-upstream vs. aux-5 V) via ideal-diode/jumper so a satellite's
  own USB plugged in for flashing doesn't back-feed the shared rail.

## RF / layout rules

- Position each socket so the board's **antenna end overhangs the PCB edge** with a ground-plane
  cutout beneath it.
- Space/orient antennas **> ~3 cm** apart for real diversity without mutual desense.
- Status LEDs, RST/BOOT access, mounting holes.

## Prototype → fab

1. **Host mode:** validate 4-stream merge on a powered USB hub (no custom HW).
2. **Standalone mode:** validate SPI aggregation on perfboard + female headers + jumpers
   (1 DevKitC + up to 3 SuperMini), including the `SYNC` line.
3. **Then** lay out the KiCad carrier (`hardware/carrier/`): USB-hub IC + SPI/SYNC bus, 4 sockets,
   power-select, antenna-keepout layout. Outputs: schematic PDF, gerbers, BOM, assembly notes.
