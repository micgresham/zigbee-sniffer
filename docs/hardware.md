# Hardware

> ## ⚠️ You need an ESP32-**C6** (or ESP32-**H2**) — NOT an ESP32-C61/C3/C2/S3/C5
> This project requires the **IEEE 802.15.4 radio**. Only the **ESP32-C6** and **ESP32-H2** have
> it. The similarly-named **ESP32-C61 is Wi-Fi 6 + BLE only and has NO 802.15.4 radio** — it
> physically cannot sniff Zigbee, regardless of firmware. (Symptom of flashing a C6 image to a
> C61: a boot loop with `ESP-ROM:esp32c61…` and `Invalid chip id. Expected 20 read 13`.)
> For the **`standalone`** WiFi web-UI build you specifically need a **C6** (the H2 has no Wi-Fi);
> the H2 works for the `usb-sniffer` / `satellite` roles only.

## Boards

### Primary — ESP32-C6-DevKitC-1
- RISC-V single core @ 160 MHz, ~320 KB HP SRAM (no PSRAM), typically **8 MB flash**.
- Native **IEEE 802.15.4** radio (Zigbee/Thread), Wi-Fi 6, BLE 5 — all sharing one 2.4 GHz
  front end and **one antenna**.
- USB Serial/JTAG (native USB), BOOT + RST buttons, WS2812 RGB LED.
- Runs the **`standalone`** build (8 MB leaves room for the embedded web UI + a larger LittleFS
  incident-log partition). Also fine as a `usb-sniffer` dongle.

### Satellites (optional, 0–3) — ESP32-C6 SuperMini
- ESP32-C6**FH4** (4 MB in-package flash), USB-C, ceramic chip antenna.
- Same `esp_ieee802154` radio → runs the **`satellite`** (or `usb-sniffer`) build with only a
  board flag changed.
- 4 MB is fine for satellite/dongle roles. As a *standalone primary* it forces smaller
  partitions (less on-device log history) — prefer the 8 MB DevKitC-1 for the primary.
- Alternates: WeAct ESP32-C6-Mini (~$3.86), bare ESP32-C6-WROOM-1 module (~$2–3). **ESP32-H2**
  is the cleanest RF satellite (no Wi-Fi to contend for the antenna), same driver API, but
  boards run >$5.

### Not recommended
- **CC2531 / CC2530:** TI silicon, separate toolchain/firmware, no comparable energy-detect, and
  a foreign stream format to merge. Use only if you already own one.

## Antenna & radio coexistence — the key caveat (CONFIRMED ON HARDWARE)

On real hardware, running on-device 802.15.4 promiscuous capture **and** the WiFi SoftAP at the
same time starves WiFi so badly that clients **cannot even associate** (`auth/assoc` fails). The
project resolves this with a hard rule: **the C6 only runs its 802.15.4 radio as a radio feeder.**
- `usb-sniffer` (USB→host) and `satellite` (SPI→primary): radio ON, WiFi off — no conflict.
- `standalone` (WiFi web-UI primary): its own radio **stays OFF**; it **requires ≥1 SPI satellite**
  to provide capture data. WiFi gets the radio to itself.

The rest of this section explains the underlying constraint:

Wi-Fi and 802.15.4 share **one radio front end and one antenna**:

- In the **`standalone`** build, Wi-Fi is active for the web UI, which **degrades capture
  fidelity** (the radio time-slices and the antenna is shared). This is expected, not a bug.
- The **`usb-sniffer`** build keeps Wi-Fi **off** → highest fidelity. Use it for serious
  diagnostics.
- A **single radio captures one channel at a time** and is briefly deaf while it processes a
  frame. For a fixed HA network, **pin to the HA channel**. Channel hopping is for survey/ED.
- The optional **satellite radios** remove these limits (e.g. one radio pinned to the HA channel
  while another sweeps the spectrum). See [multi-radio.md](multi-radio.md).

## Antenna placement (for multi-radio / carrier)

- Separate antennas by **> ~3 cm** (≈ λ/4 at 2.4 GHz) and orient them apart to get real spatial
  diversity instead of mutual desense.
- On boards with a trace/chip antenna, keep copper/ground out from under the antenna; on a
  carrier PCB the board's antenna end must **overhang the board edge** with a ground cutout.
- For a *most-sensitive* node, choose a board with a **u.FL/external antenna**.

## Flash partitions

See [`firmware/partitions.csv`](../firmware/partitions.csv): `nvs`, `phy_init`, a 1.75 MB
`factory` app, and a 2 MB `storage` (LittleFS) partition for the standalone incident log. Shrink
`storage` for 4 MB satellite boards (they don't need it — satellites don't log).

## Bill of materials (single unit)

| Item | Qty | Notes |
|------|-----|-------|
| ESP32-C6-DevKitC-1 | 1 | primary |
| USB-C cable | 1 | data-capable |
| (optional) ESP32-C6 SuperMini | 0–3 | satellites |
| (optional) carrier board + USB hub IC / SPI wiring | 1 | see [carrier.md](carrier.md) |
