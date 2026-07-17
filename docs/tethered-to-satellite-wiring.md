# Tethered C6 to Satellite Wiring Diagram

This diagram shows how to wire a **tethered/usb-sniffer primary C6** to one or more
**satellite C6** boards over SPI.

Pin mapping matches firmware:
- `firmware/src/standalone/spi_master.h` (primary pin map used by `usb-sniffer` OTA relay path)
- `firmware/src/satellite/transport_spi.h` (satellite pin map)

## 1) Single Satellite (minimum wiring)

```mermaid
flowchart LR
    subgraph P[Primary: ESP32-C6 (tethered/usb-sniffer), SPI master]
      P6[GPIO6 SCK]
      P7[GPIO7 MOSI]
      P2[GPIO2 MISO]
      P10[GPIO10 CS0]
      P20[GPIO20 DREADY0 in]
      P23[GPIO23 SYNC out]
      P5V[5V]
      PGND[GND]
    end

    subgraph S1[Satellite 1: ESP32-C6 (satellite), SPI slave]
      S16[GPIO6 SCK]
      S17[GPIO7 MOSI]
      S12[GPIO2 MISO]
      S110[GPIO4 CS]
      S13[GPIO3 DATA_READY out]
      S111[GPIO5 SYNC in]
      S15V[5V]
      S1GND[GND]
    end

    P6 --> S16
    P7 --> S17
    S12 --> P2
    P10 --> S110
    S13 --> P20
    P23 --> S111
    P5V --- S15V
    PGND --- S1GND
```

## 2) Up to 3 Satellites (full carrier-style wiring)

Shared across all satellites:
- `GPIO6` SCK
- `GPIO7` MOSI
- `GPIO2` MISO
- `GPIO23` SYNC
- `5V` and `GND`

Per-satellite control lines:
- `CS`: `GPIO10` (sat 1), `GPIO18` (sat 2), `GPIO19` (sat 3)
- `DATA_READY`: `GPIO20` (sat 1), `GPIO21` (sat 2), `GPIO22` (sat 3)

```mermaid
flowchart LR
    subgraph P[Primary: ESP32-C6 (tethered/usb-sniffer)]
      PSCK[GPIO6 SCK]
      PMOSI[GPIO7 MOSI]
      PMISO[GPIO2 MISO]
      PSYNC[GPIO23 SYNC]
      PCS1[GPIO10 CS1]
      PCS2[GPIO18 CS2]
      PCS3[GPIO19 CS3]
      PDR1[GPIO20 DR1 in]
      PDR2[GPIO21 DR2 in]
      PDR3[GPIO22 DR3 in]
      P5V[5V]
      PG[GND]
    end

    subgraph A[Satellite 1]
      ASCK[GPIO6]
      AMOSI[GPIO7]
      AMISO[GPIO2]
      ACS[GPIO4 CS]
      ADR[GPIO3 DR out]
      ASY[GPIO5 SYNC]
      A5V[5V]
      AG[GND]
    end

    subgraph B[Satellite 2]
      BSCK[GPIO6]
      BMOSI[GPIO7]
      BMISO[GPIO2]
      BCS[GPIO4 CS]
      BDR[GPIO3 DR out]
      BSY[GPIO5 SYNC]
      B5V[5V]
      BG[GND]
    end

    subgraph C[Satellite 3]
      CSCK[GPIO6]
      CMOSI[GPIO7]
      CMISO[GPIO2]
      CCS[GPIO4 CS]
      CDR[GPIO3 DR out]
      CSY[GPIO5 SYNC]
      C5V[5V]
      CG[GND]
    end

    PSCK --> ASCK
    PSCK --> BSCK
    PSCK --> CSCK
    PMOSI --> AMOSI
    PMOSI --> BMOSI
    PMOSI --> CMOSI
    AMISO --> PMISO
    BMISO --> PMISO
    CMISO --> PMISO
    PSYNC --> ASY
    PSYNC --> BSY
    PSYNC --> CSY
    PCS1 --> ACS
    PCS2 --> BCS
    PCS3 --> CCS
    ADR --> PDR1
    BDR --> PDR2
    CDR --> PDR3
    P5V --- A5V
    P5V --- B5V
    P5V --- C5V
    PG --- AG
    PG --- BG
    PG --- CG
```

## Connection Table (all necessary nets)

| Net | Primary (tethered C6) | Satellite pin | Required | Direction |
|---|---|---|---|---|
| SCK | GPIO6 | GPIO6 | Yes | Primary -> Satellite |
| MOSI | GPIO7 | GPIO7 | Yes | Primary -> Satellite |
| MISO | GPIO2 | GPIO2 | Yes | Satellite -> Primary |
| CS1 | GPIO10 | GPIO4 (sat 1) | Yes (sat 1) | Primary -> Satellite |
| CS2 | GPIO18 | GPIO4 (sat 2) | Optional (sat 2) | Primary -> Satellite |
| CS3 | GPIO19 | GPIO4 (sat 3) | Optional (sat 3) | Primary -> Satellite |
| DREADY1 | GPIO20 | GPIO3 (sat 1) | Yes (sat 1) | Satellite -> Primary |
| DREADY2 | GPIO21 | GPIO3 (sat 2) | Optional (sat 2) | Satellite -> Primary |
| DREADY3 | GPIO22 | GPIO3 (sat 3) | Optional (sat 3) | Satellite -> Primary |
| SYNC | GPIO23 | GPIO5 | Yes | Primary -> Satellite |
| Power | 5V | 5V | Yes | Power rail |
| Ground | GND | GND | Yes | Common reference |

## Wiring Notes

- Use a **common ground** for every board.
- Keep SPI wires short and paired with nearby ground where possible.
- Shared `MISO` is safe because only the selected satellite drives it while its `CS` is active.
- Avoid using C6 strapping pins for custom remaps unless you also update firmware pin defines.
- Satellite `radio_id` values should be unique (`1..3`) when using multiple satellites.
- **The satellite's own CS/SYNC pins were originally GPIO10/GPIO11 — verify against your
  board before wiring.** Common "ESP32-C6 SuperMini" boards (e.g. the MakerGO variant) only
  break out GPIO0–9 and GPIO12–23; **GPIO10 and GPIO11 have no pad at all**. The satellite
  firmware now uses **GPIO4 (CS)** and **GPIO5 (SYNC)** instead — both confirmed broken out.
  If your specific SuperMini variant differs, cross-check its silkscreen/pinout before
  wiring, and update `firmware/src/satellite/transport_spi.h` to match.