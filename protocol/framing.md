# Wire framing protocol (v1)

This is the **single source of truth** for the binary protocol used on every transport:
USB Serial/JTAG (dongle), UART (simple satellite), SPI (carrier satellite), and — re-encoded
as length-prefixed binary over a WebSocket — the on-device `standalone` web UI.

A machine-readable summary lives in [`framing.json`](framing.json). Keep both in sync.

All multi-byte integers are **little-endian**.

---

## Frame structure

```
+--------+--------+--------+--------+--------+-----------------+--------+--------+
| MAGIC0 | MAGIC1 |  VER   |  TYPE  |     LEN (u16)   | PAYLOAD (LEN bytes) | CRC16 (u16) |
+--------+--------+--------+--------+-----------------+-----------------+-------------+
  0x5A     0xBE      0x01    type      len_lo len_hi   ............         crc_lo crc_hi
```

| Field   | Size | Notes |
|---------|------|-------|
| MAGIC0  | 1    | `0x5A` |
| MAGIC1  | 1    | `0xBE` |
| VER     | 1    | protocol version, currently `0x01` |
| TYPE    | 1    | message type (see below) |
| LEN     | 2    | payload length in bytes (u16 LE), `0..2048` |
| PAYLOAD | LEN  | type-specific |
| CRC16   | 2    | CRC-16/CCITT-FALSE (poly `0x1021`, init `0xFFFF`) over `VER,TYPE,LEN,PAYLOAD` |

A receiver resynchronises by scanning for `0x5A 0xBE` and validating VER + CRC. Frames that
fail CRC are counted (`dropped_crc`) and discarded.

---

## Message types

### Device → host  (`0x0_`)

#### `0x01 CAPTURED_FRAME`
A single 802.15.4 PHY frame as received.

| Field      | Size | Notes |
|------------|------|-------|
| radio_id   | 1    | `0`=primary, `1..3`=satellites |
| channel    | 1    | `11..26` |
| rssi       | 1    | int8, dBm |
| lqi        | 1    | `0..255` |
| flags      | 1    | bit0 `crc_ok`, bit1 `promiscuous`, bit2 `frame_pending`, bit3 `was_acked` |
| timestamp  | 8    | u64 microseconds; primary-synced when the `SYNC` line is used, else local |
| mpdu_len   | 1    | length of the following MPDU |
| mpdu       | N    | raw 802.15.4 MPDU (MHR + payload; **includes** the 2-byte FCS) |

#### `0x02 ED_RESULT`
One energy-detect sample (the spectrum primitive). Many of these form a sweep.

| Field      | Size | Notes |
|------------|------|-------|
| radio_id   | 1    | |
| channel    | 1    | `11..26` |
| timestamp  | 8    | u64 µs |
| ed_dbm     | 1    | int8, energy in dBm (approx; see `docs/spectrum.md`) |
| sweep_id   | 2    | u16, increments per full sweep so the UI can group samples |

#### `0x03 STATUS`
Periodic device status / heartbeat.

| Field        | Size | Notes |
|--------------|------|-------|
| radio_id     | 1    | |
| mode         | 1    | see `mode` enum |
| channel      | 1    | current channel |
| hop_mask     | 4    | u32 bitmask of channels in the hop set (bit i = channel 11+i) |
| hop_dwell_ms | 2    | dwell per channel when hopping (0 = fixed channel) |
| uptime_s     | 4    | u32 seconds |
| captured     | 4    | u32 frames captured since boot |
| dropped_crc  | 4    | u32 frames dropped on CRC |
| dropped_buf  | 4    | u32 frames dropped on buffer overflow (backpressure) |
| fw_major     | 1    | |
| fw_minor     | 1    | |

#### `0x04 LOG`
Free-text log line (UTF-8) for diagnostics. Payload = bytes of the message.

#### `0x05 ACK`
Acknowledges a command. Payload: `cmd_type(1)`, `result(1)` (`0`=ok, nonzero=error code).

#### `0x06 INCIDENT`
A detected diagnostic incident (e.g. a device went silent). Payload is a **UTF-8 JSON** object,
e.g. `{"ts":1234,"addr":"0x1234","reason":"silence","rssi":-72,"lqi":110,"ch":15,"ed":-88,"silent_s":130}`.
The standalone build also persists incidents to flash and serves them at `GET /incidents`.
See [docs/incidents.md](../docs/incidents.md).

#### `0x07 PROBE_RESULT`
Result of an active MAC probe (see `CMD_PROBE`). Payload:
`radio_id(1)` `target(2, LE short addr)` `acked(1)` `rssi(int8, ACK RSSI)` `lqi(1, ACK LQI)`.
`acked=1` means the target's 802.15.4 MAC auto-ACKed — its radio is alive and on-channel.

#### `0x08 OTA_STATUS`
Firmware-update progress (see `CMD_OTA_*` and [docs/ota.md](../docs/ota.md)). Payload:
`target(1)` `state(1)` `received(4, LE)` `total(4, LE)` `err(1)`.
`state`: 0 idle · 1 receiving · 2 writing · 3 verifying · 4 ok(rebooting) · 5 error.

---

### Host → device  (`0x8_`)

| Type | Name         | Payload |
|------|--------------|---------|
| `0x81` | `CMD_SET_CHANNEL` | `channel(1)` `11..26` |
| `0x82` | `CMD_SET_MODE`    | `mode(1)` |
| `0x83` | `CMD_START`       | — |
| `0x84` | `CMD_STOP`        | — |
| `0x85` | `CMD_SET_HOP`     | `hop_mask(4)` `hop_dwell_ms(2)` |
| `0x86` | `CMD_ED_SCAN`     | `hop_mask(4)` (channels to sweep) `dwell_ms(2)` |
| `0x87` | `CMD_SET_KEY`     | `key_present(1)` then `key(16)` if present (Zigbee NWK key; stored in NVS) |
| `0x88` | `CMD_GET_STATUS`  | — (device replies with `STATUS`) |
| `0x89` | `CMD_SET_RADIO_ID`| `radio_id(1)` (satellite provisioning) |
| `0x8A` | `CMD_PROBE`       | `target(2, LE)` `pan(2, LE)` — TX a MAC frame to `target`, await ACK (active test). Device replies with `PROBE_RESULT`. |
| `0x8B` | `CMD_OTA_BEGIN`   | `target(1)` `total_size(4, LE)` `img_crc32(4, LE)` — start a firmware update (target 0 = this C6, 1..3 = satellite over SPI) |
| `0x8C` | `CMD_OTA_DATA`    | `target(1)` `offset(4, LE)` `chunk(n)` — a firmware chunk (≤512 B) |
| `0x8D` | `CMD_OTA_END`     | `target(1)` — finish, verify, set boot slot, reboot |
| `0x8E` | `CMD_OTA_ABORT`   | `target(1)` — cancel an in-progress update |

---

## Enums

### `mode`
| Value | Name | Meaning |
|-------|------|---------|
| 0 | `IDLE` | radio stopped |
| 1 | `CAPTURE` | promiscuous capture on the configured channel(s) |
| 2 | `ED_SWEEP` | continuous energy-detect sweep (spectrum) |
| 3 | `CAPTURE_PLUS_ED` | interleave capture with periodic ED (single-radio compromise) |

### `flags` (CAPTURED_FRAME)
| Bit | Name | |
|-----|------|--|
| 0 | `crc_ok` | FCS validated |
| 1 | `promiscuous` | captured in promiscuous mode |
| 2 | `frame_pending` | FCF frame-pending bit |
| 3 | `was_acked` | an ACK for this frame was also observed |

---

## Versioning

Bump `VER` on any breaking change to a payload layout. Host and firmware both reject mismatched
major versions and emit a `LOG` describing the expected/received version.
