# Firmware

PlatformIO + ESP-IDF project under [`firmware/`](../firmware/). Three build profiles share one
capture core.

## Layout

```
firmware/
├── platformio.ini            # 3 envs; build_src_filter selects core + one build dir
├── sdkconfig.defaults        # shared IDF config
├── sdkconfig.{usb-sniffer,standalone,satellite}   # per-profile overrides
├── partitions.csv
└── src/
    ├── core/                 # SHARED: radio_capture, ed_scan, codec, config_nvs
    ├── usb-sniffer/          # app_main + transport_usb  (Block A — done)
    ├── standalone/           # app_main (+ WiFi/httpd/UI in Block B)
    └── satellite/            # app_main (+ SPI forward in Block D)
```

## Builds

```bash
pio run -e usb-sniffer            # build
pio run -e usb-sniffer -t upload  # build + flash
pio run -e usb-sniffer -t monitor # serial monitor (console on UART0 for this env)
```
Swap `-e` for `standalone` or `satellite`.

## Capture core (`src/core`)

- **`radio_capture.c`** wraps `esp_ieee802154`: promiscuous RX, per-frame RSSI/LQI/channel/
  timestamp via the `esp_ieee802154_receive_done()` callback, pushed to a FreeRTOS queue. The
  callback stays short; the app task drains the queue. A drop counter tracks queue-full events.
- **`ed_scan.c`** wraps `esp_ieee802154_energy_detect()` (async → made synchronous with a
  semaphore) and sweeps a channel mask — the RF spectrum primitive. See [spectrum.md](spectrum.md).
- **`codec.c`** implements the [wire framing](../protocol/framing.md): `zb_encode*()` and an
  incremental `zb_decoder_t`. CRC-16/CCITT-FALSE. Pure C, mirrored by `host/zbsniff/proto.py`.
- **`probe.c`** (`CMD_PROBE`) TX's a MAC data frame to a target's short address with the
  ACK-request bit set — an active device liveness test. See [user-guide.md](user-guide.md).
- **`beacon.c`** (`CMD_BEACON_REQ`, firmware ≥ 0.26) TX's an 802.15.4 beacon request so
  neighbouring coordinators/routers reply with a beacon revealing their PAN — the active
  network-discovery scan. See [networks.md](networks.md).
- **`config_nvs.c`** persists channel/mode/hop/network-key/Wi-Fi settings in NVS.

## usb-sniffer build (Block A)

`app_main.c` runs a capture loop (encode `CAPTURED_FRAME` → USB) plus a command task that parses
host commands (`CMD_SET_CHANNEL`, `CMD_SET_MODE`, `CMD_START/STOP`, `CMD_SET_HOP`, `CMD_ED_SCAN`,
`CMD_SET_KEY`, `CMD_GET_STATUS`, `CMD_PROBE`, `CMD_BEACON_REQ`, `CMD_OTA_*`). A ~1 Hz `STATUS`
heartbeat reports counters.

### Console vs. binary stream
The binary protocol uses the USB Serial/JTAG peripheral. To avoid `ESP_LOG` output corrupting it,
`sdkconfig.usb-sniffer` routes the **console to UART0** (`CONFIG_ESP_CONSOLE_UART_DEFAULT`).
Watch logs on UART0, or rely on `MSG_LOG` frames forwarded by the host.

## FCS handling — verify on hardware

Whether the radio delivers the 2-byte FCS in the PSDU affects the Wireshark TAP `FCS type` TLV.
The firmware passes through whatever the driver returns; the host pcap writer assumes FCS present
(`--no-fcs` to override). Confirm on first capture and adjust if Wireshark flags malformed FCS.
See [troubleshooting.md](troubleshooting.md).

## PlatformIO sdkconfig pitfalls (important)

1. **Don't name a defaults file `sdkconfig.<envname>`.** PlatformIO *generates* a file with
   exactly that name per environment and will clobber your hand-written one. Per-env overrides
   live in [`sdkconfigs/<env>.defaults`](../firmware/sdkconfigs/). The generated `sdkconfig.<env>`
   files are gitignored.

2. **`board_build.sdkconfig_defaults` does nothing in this PlatformIO/ESP-IDF integration.**
   (Verified 2026-07: the key appears nowhere in `espidf.py`, and the three generated
   `sdkconfig.<env>` files were byte-for-byte identical despite each `sdkconfigs/<env>.defaults`
   intending real per-env differences.) A previous version of this doc claimed it worked for
   "console/system options" but not "component options" — that was wrong; it silently applied to
   **none** of them. The setting that actually works is ESP-IDF's own `SDKCONFIG_DEFAULTS` CMake
   variable, reachable via the *documented, verified-working* hook:
   ```ini
   board_build.cmake_extra_args = -DSDKCONFIG_DEFAULTS="sdkconfig.defaults;sdkconfigs/<env>.defaults"
   ```
   `sdkconfig.defaults` (shared) then `sdkconfigs/<env>.defaults` (per-env) are merged in order,
   later files winning — confirmed by diffing the three generated sdkconfigs, which now actually
   differ (flash size, console routing).

3. **Flash size: two independent settings must both be right, and neither is "auto."** The image
   **header's** flash-size field — the one the bootloader's boot-time probe checks against the
   real chip — comes from `board.upload.flash_size` (ini: `board_upload.flash_size`, read by
   `elf2image` in `main.py`), **not** from any sdkconfig value. `CONFIG_ESPTOOLPY_FLASHSIZE` (from
   sdkconfig, fixed via #2 above) only affects the *app's own* runtime view. A previous version of
   this doc claimed "esptool auto-detects the real size" — that's false for this field; whatever
   `board_upload.flash_size` says gets baked in at build time, unconditionally. Get it wrong (e.g.
   the shared 8 MB default leaking onto a 4 MB SuperMini satellite) and the bootloader crash-loops:
   ```
   E spi_flash: Detected size(4096k) smaller than the size in the binary image header(8192k). Probe failed.
   assert failed: __esp_system_init_fn_init_flash ... (flash_ret == ESP_OK)
   ```
   `[env:satellite]` in `platformio.ini` sets `board_upload.flash_size = 4MB` (image header) *and*
   `sdkconfigs/satellite.defaults` sets `CONFIG_ESPTOOLPY_FLASHSIZE_4MB=y` (app view, via #2) —
   both are needed; verify with `python3 -c` reading byte 3 of `firmware.bin`/`bootloader.bin`
   (bits 4-7: 0=1MB 1=2MB 2=4MB 3=8MB 4=16MB), not just the sdkconfig.

## Console routing per build

| Build | Console | Port to watch |
|-------|---------|---------------|
| `standalone` | UART0 | the **`UART`** port (USB-UART bridge) — reliable across resets |
| `usb-sniffer` | UART0 | the **`UART`** port (USB carries the binary protocol) |
| `satellite` | UART0 | the **`UART`** port (USB free; SPI is the data path) |

The DevKitC-1 has **two USB-C ports**: `UART` (bridge → UART0) and `USB` (native USB-Serial/JTAG).
All builds now log to UART0, so always monitor the **`UART`** port for logs.

## Partition table

`partitions.csv` (1.75 MB `factory` app + 2 MB `storage`/SPIFFS for the incident log) is wired
in via **`board_build.partitions`** in `platformio.ini`. Note: the `CONFIG_PARTITION_TABLE_CUSTOM`
sdkconfig flag alone is *not* enough under PlatformIO — `board_build.partitions` is what actually
applies it (without it you get the default ~1 MB app and no `storage` partition, so SPIFFS fails).

## Adding a command

1. Add the type to [`protocol/framing.md`](../protocol/framing.md) + `framing.json`.
2. Mirror it in `firmware/src/core/proto.h` and `host/zbsniff/proto.py`.
3. Handle it in the relevant `app_main.c` (`handle_command`) and add a builder in `proto.py`.
