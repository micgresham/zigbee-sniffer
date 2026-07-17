# Firmware OTA update (design)

Goal: update firmware **without USB reflashing** — for the tethered C6 over its serial link, and
for **satellite units over SPI** (they have no easy USB access on a carrier board). Both paths are
implemented and validated on hardware — see [Status / staging](#status--staging) below.

## Why OTA
- The tethered C6 *can* be reflashed over USB, but OTA is convenient and consistent.
- **Satellites are the real driver:** mounted on a carrier and connected to the host C6 by SPI,
  they can't be flashed with esptool without unplugging. They must receive firmware **relayed
  through the tethered/host C6 over SPI**.

## Topology

```
  Go host  ──serial(framed)──►  Tethered C6  ──SPI──►  Satellite C6 (×1..3)
   (.bin)      OTA_* msgs         self-OTA               SPI-OTA
                                  or relay
```

- **Target 0** = the tethered C6 itself (writes its own OTA partition).
- **Target 1..3** = a satellite (the tethered C6 relays each chunk over SPI to that satellite).

## Partition layout (required firmware change)

Both the tethered and satellite builds need an **OTA-capable partition table** (currently a single
app partition). Minimum: `otadata` + two app slots (`ota_0`, `ota_1`) that each fit the app. On the
DevKitC-1 (8 MB) and SuperMini (4 MB) there is room. `partitions.csv`:

```
# name,     type, subtype, offset,   size
nvs,        data, nvs,     0x9000,   0x6000
otadata,    data, ota,     0xf000,   0x2000
phy_init,   data, phy,     0x11000,  0x1000
ota_0,      app,  ota_0,   0x20000,  0x1C0000
ota_1,      app,  ota_1,   0x1E0000, 0x1C0000
```

The running slot writes the *other* slot, verifies, sets it bootable (`esp_ota_set_boot_partition`),
and reboots; `otadata` + the bootloader roll back automatically if the new image fails to confirm.

## Wire protocol (host → device `0x8_`, device → host `0x0_`)

| Type | Name | Payload |
|------|------|---------|
| `0x8B` | `CMD_OTA_BEGIN` | `target(1)` `total_size(4)` `img_crc32(4)` |
| `0x8C` | `CMD_OTA_DATA`  | `target(1)` `offset(4)` `chunk(n≤512)` |
| `0x8D` | `CMD_OTA_END`   | `target(1)` |
| `0x8E` | `CMD_OTA_ABORT` | `target(1)` |
| `0x08` | `MSG_OTA_STATUS`| `target(1)` `state(1)` `received(4)` `total(4)` `err(1)` |

`state`: 0 idle · 1 receiving · 2 writing · 3 verifying · 4 ok(rebooting) · 5 error.
Chunks are acked implicitly by periodic `MSG_OTA_STATUS` (received/total → progress bar).

## Flow

**Tethered self-update (target 0)**
1. Host reads the `.bin`, sends `CMD_OTA_BEGIN(0,size,crc)`.
2. Firmware `esp_ota_begin()` on the next slot; streams `CMD_OTA_DATA` → `esp_ota_write()`.
3. `CMD_OTA_END` → `esp_ota_end()` + CRC/verify → `esp_ota_set_boot_partition()` → reboot.
4. Host watches `MSG_OTA_STATUS`; on `ok` the device reboots and re-enumerates (the host's
   auto-reconnect loop re-attaches).

**Satellite update over SPI (target 1..3)**
1. Same `CMD_OTA_BEGIN/DATA/END` but `target=id`.
2. The tethered C6 **does not** write its own flash; it **relays** each chunk to satellite `id`
   over the existing SPI transport (a new `SPI_OTA_*` opcode), and forwards the satellite's status
   back as `MSG_OTA_STATUS`.
3. The satellite writes the raw partition directly (offset-deduped, incrementally erased — see
   status below), verifies once, and reboots; the master re-syncs it.

## Host pipeline (implemented first)
- `POST /api/ota?target=<0..3>` with the `.bin` body → chunk over serial (≤512 B), emit progress
  on the WebSocket (`{kind:"ota", target, received, total, state}`).
- UI: **Config → Firmware update** — pick a `.bin`, choose target (This C6 / Satellite 1..3), a
  progress bar, and the current running version (from `MSG_STATUS` fw field).

## Safety
- CRC32 over the whole image; the device verifies before switching slots.
- Bootloader **rollback** protects against a bad image (the old slot stays bootable until the new
  one confirms).
- OTA is gated behind an explicit user action; capture is paused during a self-update.

## Status / staging
1. **Protocol + host `/api/ota` + UI progress** — ✅ implemented (`CMD_OTA_*` / `MSG_OTA_STATUS`,
   `POST /api/ota`, Config → Firmware update panel with a live progress bar over the WebSocket).
2. **Tethered self-OTA firmware** — ✅ implemented (`core/ota.c` using `esp_ota`, `partitions_ota.csv`
   with two app slots wired into the `tethered` env). **Needs on-hardware validation** — flash once
   over USB with the new OTA partition table, then subsequent updates can go over serial.
3. **Satellite SPI relay + SPI-OTA receive** — ✅ implemented and **validated on carrier hardware**,
   including transfers with heavy chunk-retry activity throughout: the tethered firmware brings up
   an SPI master and relays `target` 1..3 OTA chunks (`ota_relay` → `spi_master_send_to`). The
   satellite no longer uses `esp_ota_write()` (its stateful internal write-pointer offered no way to
   detect a resent chunk, and a lost acknowledgment — not lost data — could silently double-write
   and corrupt everything after it); it now writes the raw target partition directly
   (`esp_partition_write`), erasing incrementally per-sector as data arrives rather than the whole
   image up front (a single multi-second erase was long enough to trip the watchdog on this
   single-core chip), and verifies the whole image once via `esp_image_verify` right before
   switching boot partitions. Each `CMD_OTA_DATA` carries its byte offset, and the satellite skips
   any chunk whose offset is already applied — SPI is lossy enough that most transfers need many
   resends, and this makes a resend always safe. The host uses 200-byte chunks for satellites so
   each fits one 256-byte SPI transaction. The SPI master is only initialised on the first
   satellite-OTA request, so the normal single-radio path is untouched.

> USB reflashing remains the always-available fallback and cannot brick the device.
