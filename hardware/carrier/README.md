# Carrier board

Design for the optional multi-radio carrier (1 primary + up to 3 satellites). **Prototype on a
powered USB hub / perfboard first** (see [../../docs/carrier.md](../../docs/carrier.md)).

## Contents (fabrication-ready design package)

- **[DESIGN.md](DESIGN.md)** — full schematic-level design: blocks, power, SPI/SYNC bus, the
  USB-hub-vs-SPI variants, and RF/layout rules.
- **[netlist.csv](netlist.csv)** — exact net-by-net connection table.
- **[bom.csv](bom.csv)** — bill of materials.
- **[pinout.md](pinout.md)** — primary + satellite GPIO map (matches the firmware `#define`s).

## On the KiCad files

The binary CAD files (`carrier.kicad_sch` / `carrier.kicad_pcb` / gerbers) are **drawn from this
spec in KiCad** — they aren't committed yet because a valid KiCad layout can't be reliably
hand-authored outside the tool. The netlist + BOM + pinout here are complete enough to:
1. place the 1 DevKitC + 3 SuperMini board footprints (with antenna-edge overhang),
2. import the netlist to create the ratsnest,
3. route the shared SPI bus + per-satellite CS/DATA_READY + SYNC + the 5 V/GND planes,
4. export gerbers.

Firmware pin assignments live in `firmware/src/satellite/transport_spi.h` and
`firmware/src/standalone/spi_master.h` — keep them in sync with [pinout.md](pinout.md).
