# Carrier board — design specification

A fabrication-ready design for the optional multi-radio carrier (1 primary + up to 3 satellites).
This is the engineering spec (schematic-level netlist, BOM, pinout, layout rules) from which the
KiCad `.kicad_sch` / `.kicad_pcb` are drawn — see [README.md](README.md) for why the binary CAD
files are produced from this spec rather than hand-authored.

The firmware pin assignments below match the `#define`s in
`firmware/src/satellite/transport_spi.h` and `firmware/src/standalone/spi_master.h`.

## Two carrier variants

### A. Standalone SPI-aggregator carrier (the integrable PCB)
Primary C6 (SPI master) + up to 3 satellite C6 (SPI slaves) wired over **GPIO headers** — fully
realizable as one PCB because it only needs the boards' exposed GPIO/5V/GND pins. This is the
board the netlist/BOM here describe.

### B. USB-hub option (host mode)
For full host-mode fidelity the recommended path is the boards plugged into an **off-the-shelf
powered USB hub** → the HA/host box (the Python host merges the serial streams; firmware
unchanged). An *integrated* USB-hub PCB would need each board's USB **D+/D-** brought out on
headers — the **ESP32-C6 SuperMini does not expose these** (USB is on its USB-C connector only),
so an integrated hub carrier isn't generally feasible with SuperMinis. Use the external powered
hub instead (it's also the Block-D prototype path). If you select boards that *do* expose USB
D+/D- on pins, an onboard FE1.1s 4-port hub can be added — pinout in the BOM notes.

---

## Block diagram (variant A)

```
        ┌──────── ESP32-C6-DevKitC-1 (PRIMARY, SPI master) ────────┐
        │  SCK6  MOSI7  MISO2   CS:10/18/19   DREADY:20/21/22  SYNC:23
        └──┬──────┬──────┬─────────┬───┬───┬──────┬───┬───┬─────┬──┘
   shared  │ SCK  │ MOSI │ MISO    │CS0│CS1│CS2   │DR0│DR1│DR2  │ SYNC (fan-out)
   bus ────┼──────┼──────┼────┐    │   │   │      │   │   │     │
        ┌──┴──────┴──────┴──┐ │  ┌─┘   │   │   ┌──┘   │   │     │
        │  Satellite 1      │ │  │     │   │   │      │   │     │
        │  SCK6 MOSI7 MISO2 │ │  │CS10 │   │   │DR3   │   │     │
        │  CS10  DREADY3  SYNC11 ◄──────┼───┼──────────────────┘ (SYNC to all)
        └───────────────────┘ │  └─────┘   │   └──────┘
        (Satellites 2,3 identical, on shared SCK/MOSI/MISO, own CS_i + DREADY_i)
```

- **Shared** across all boards: `SCK`, `MOSI`, `MISO`, `GND`, `5V`.
- **Per-satellite**: one `CS` (primary→sat) and one `DATA_READY` (sat→primary).
- **Broadcast**: `SYNC` (primary→all satellites) for timestamp alignment.

---

## Power

- Single **5 V** source (carrier USB-C or barrel) → every board's **5V** pin (each board's
  onboard LDO regulates 3V3). Common **GND**.
- Budget ≈ 1 A (RX-only satellites ~0.1–0.2 A each + primary). A 5 V / 2 A supply is ample.
- **Power-source select**: a slide switch / ideal-diode-OR (e.g. 2× Schottky or a load-switch)
  so a board's own USB plugged in for flashing does not back-feed the shared 5 V rail.
- 100 nF decoupling per board 3V3 pin + a 100–470 µF bulk cap on the 5 V rail.

## RF / layout rules (critical)

- Each board socket positioned so the board's **antenna end overhangs the carrier PCB edge**,
  with a **ground-plane keep-out** under the antenna.
- Space board antennas **> ~3 cm** apart and orient them outward for diversity without desense.
- Keep the SPI bus traces short; series ~33 Ω on `SCK`/`MOSI` optional for signal integrity at
  longer trace lengths; the 2 MHz default clock is conservative.

## Connectors / mechanical

- 4× female socket headers matching the board pinouts (1× DevKitC-1, 3× identical SuperMini).
- Flash each board **before** seating, or include per-board power jumpers.
- Status LEDs (power, per-satellite DATA_READY) optional; RST/BOOT access cut-outs; M2.5 mounting
  holes at the corners.

See [netlist.csv](netlist.csv), [bom.csv](bom.csv), and [pinout.md](pinout.md) for the exact
connections, parts, and pin map.
