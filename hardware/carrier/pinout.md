# Carrier pinout (matches firmware)

GPIO assignments below mirror the firmware `#define`s — keep them in sync:
`firmware/src/satellite/transport_spi.h` and `firmware/src/standalone/spi_master.h`.

## Primary (ESP32-C6-DevKitC-1, SPI master)

| Function | GPIO |
|----------|------|
| SPI SCK | 6 |
| SPI MOSI | 7 |
| SPI MISO | 2 |
| CS → satellite 1 | 10 |
| CS → satellite 2 | 18 |
| CS → satellite 3 | 19 |
| DATA_READY ← satellite 1 | 20 |
| DATA_READY ← satellite 2 | 21 |
| DATA_READY ← satellite 3 | 22 |
| SYNC → all satellites | 23 |

## Satellite (ESP32-C6 SuperMini, SPI slave) — all three identical

| Function | GPIO |
|----------|------|
| SPI SCK | 6 |
| SPI MOSI | 7 |
| SPI MISO | 2 |
| CS (from primary) | 4 |
| DATA_READY (to primary) | 3 |
| SYNC (from primary) | 5 |

Each satellite is provisioned with a distinct **radio_id (1, 2, 3)** in NVS (firmware
`CMD_SET_RADIO_ID`, or the primary can assign on enumerate).

## Notes
- **CS and SYNC were originally GPIO10/GPIO11** — common "ESP32-C6 SuperMini" boards only break
  out GPIO0–9 and GPIO12–23, so those two pins have **no pad at all** on real hardware. Moved to
  GPIO4/GPIO5 instead. Those are JTAG strapping pins (MTMS/MTDI) on this chip, but that only
  matters if you're actively using JTAG debugging — as plain GPIOs post-boot they're fine, and
  they're the pins actually broken out. Verify against your specific SuperMini variant before
  wiring; adjust the `#define`s + this table together if it differs.
- The shared `MISO` is only driven by the satellite whose `CS` is asserted; others tri-state.
- **Verified against an actual PADS/EasyEDA netlist (2026-07-11)** for a 3-satellite carrier:
  every shared bus line (SCK/MOSI/MISO/SYNC) and per-satellite line (CS/DATA_READY) matched this
  table exactly, with satellites resolved to radio_id 1/2/3 via CS on GPIO10/18/19 and
  DATA_READY on GPIO20/21/22 respectively on the primary side. See `netlist.csv`.
