# Channel reference (Zigbee 2.4 GHz ↔ Wi-Fi overlap)

IEEE 802.15.4 / Zigbee uses channels **11–26** in the 2.4 GHz band, 5 MHz apart, each ~2 MHz
wide. Wi-Fi channels are ~20–22 MHz wide, so each Wi-Fi channel overlaps several Zigbee channels.
This table drives the **interference advisor** ([spectrum.md](spectrum.md)).

| Zigbee ch | Center (MHz) | Overlaps Wi-Fi (2.4 GHz) |
|-----------|--------------|--------------------------|
| 11 | 2405 | 1 |
| 12 | 2410 | 1 |
| 13 | 2415 | 1, 2 |
| 14 | 2420 | 1, 2, 3 |
| 15 | 2425 | 2, 3, 4 (clear of Wi-Fi 1) |
| 16 | 2430 | 3, 4, 5 |
| 17 | 2435 | 4, 5, 6 |
| 18 | 2440 | 5, 6, 7 |
| 19 | 2445 | 6, 7, 8 |
| 20 | 2450 | 7, 8, 9 |
| 21 | 2455 | 8, 9, 10 |
| 22 | 2460 | 9, 10, 11 |
| 23 | 2465 | 10, 11, 12 |
| 24 | 2470 | 11, 12, 13 |
| 25 | 2475 | 13 (clear of Wi-Fi 11 in US) |
| 26 | 2480 | 13, 14 (often clear in US; edge of band) |

## Practical guidance

- The Zigbee channels that best dodge the common US Wi-Fi channels **1 / 6 / 11** are
  **15, 20, 25** (and **26**, though some older devices avoid it and TX power may be capped).
- Find your HA channel: **ZHA** → Network settings, or **Zigbee2MQTT** → Settings → Network.
- **Pin the sniffer to your HA channel** for diagnostics. Use hopping/ED only for a site survey
  to pick a quieter channel.
- Wi-Fi is not the only interferer — microwave ovens, USB 3.0 / cabling, BLE, and analog video
  senders all sit in 2.4 GHz. The ED sweep sees them all as raised noise.
