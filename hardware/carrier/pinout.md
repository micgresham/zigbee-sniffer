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
| CS (from primary) | 10 |
| DATA_READY (to primary) | 3 |
| SYNC (from primary) | 11 |

Each satellite is provisioned with a distinct **radio_id (1, 2, 3)** in NVS (firmware
`CMD_SET_RADIO_ID`, or the primary can assign on enumerate).

## Notes
- These GPIOs avoid the C6 strapping pins (4, 5, 8, 9, 15) and the USB-JTAG pins (12, 13).
- Verify against your specific SuperMini variant's exposed pins; adjust the `#define`s + this
  table together if a pin isn't broken out.
- The shared `MISO` is only driven by the satellite whose `CS` is asserted; others tri-state.
