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
- **`config_nvs.c`** persists channel/mode/hop/network-key/Wi-Fi settings in NVS.

## usb-sniffer build (Block A)

`app_main.c` runs a capture loop (encode `CAPTURED_FRAME` → USB) plus a command task that parses
host commands (`CMD_SET_CHANNEL`, `CMD_SET_MODE`, `CMD_START/STOP`, `CMD_SET_HOP`, `CMD_ED_SCAN`,
`CMD_SET_KEY`, `CMD_GET_STATUS`). A ~1 Hz `STATUS` heartbeat reports counters.

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

Two non-obvious things that will bite you:

1. **Don't name a defaults file `sdkconfig.<envname>`.** PlatformIO *generates* a file with
   exactly that name per environment and will clobber your hand-written one. Per-env overrides
   live in [`sdkconfigs/<env>.defaults`](../firmware/sdkconfigs/) and are referenced via
   `board_build.sdkconfig_defaults`. The generated `sdkconfig.<env>` files are gitignored.
2. **PlatformIO's secondary `sdkconfig_defaults` file is unreliable for *component* options**
   (e.g. `CONFIG_HTTPD_WS_SUPPORT`). Console/system options applied from it, but component ones
   silently didn't. Put anything that must stick (WS support, flash size) in the shared
   **`sdkconfig.defaults`**, which is applied reliably.
3. **Flash size must match the board + partition extent.** The DevKitC-1 is 8 MB; a 2 MB setting
   with a ~4 MB partition table both errors config generation *and* makes the SPIFFS partition
   invalid at runtime. `sdkconfig.defaults` pins `CONFIG_ESPTOOLPY_FLASHSIZE_8MB` (a 4 MB
   SuperMini still flashes — esptool auto-detects the real size).

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
