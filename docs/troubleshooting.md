# Troubleshooting

## Build / flash

- **First `pio run` is slow / downloads a lot** — it's fetching the ESP-IDF toolchain. Normal.
- **Upload fails / port busy** — close any serial monitor or Wireshark bridge first. Hold **BOOT**,
  tap **RST**, release BOOT to force the bootloader, then retry.
- **`pio run -e standalone` complains about `web_assets/index.html.gz`** — that embedded asset is
  a placeholder until Block B; build `-e usb-sniffer` for now.

## Serial terminal goes quiet after the boot loader (`entry 0x…`)

Expected for the **`usb-sniffer`** build (which is the **default** `pio run` env). That build
routes console logs to **UART0** and uses **USB Serial/JTAG for the binary capture protocol**, so a
text terminal shows the ROM boot lines and then only occasional non-printable bytes. It is not
hung. Verify with the host tool (close the serial monitor first — the port is single-owner):

```bash
python -m zbsniff.tools.listen --port <port> --channel <your HA channel>
```

`STATUS` lines every ~1 s prove the firmware is running; `FRAME` lines appear once there's traffic
on that channel. For a human-readable console + WiFi UI instead, flash the **standalone** build
(`pio run -e standalone -t upload`) and browse to its AP (`zb-sniffer` → http://192.168.4.1); that
build *does* log to USB.

## `Guru Meditation ... Stack protection fault` in task "main" at boot

Main-task stack overflow. Two causes were fixed and are worth knowing if you add to `app_main`:
- **Oversized stack buffers.** Use `ZB_MAX_TX` (256 B payload) for buffers holding messages the
  firmware *originates*, not `ZB_MAX_PAYLOAD` (2 KB) — a 2 KB local plus WiFi init overflows the
  main stack. Large `zb_decoder_t` structs (~2 KB) in a task should be `static`, not stack locals.
- **Main task stack size.** Standalone brings up WiFi + httpd + SPIFFS in the main task;
  `sdkconfig.defaults` sets `CONFIG_ESP_MAIN_TASK_STACK_SIZE=8192` for headroom.

The decoded backtrace shows `Stack pointer` below the `Stack bounds` lower limit — that's the
tell-tale overflow signature.

## "host up — no device data" (link goes quiet, device is fine)

The ESP32-C6 USB Serial/JTAG link can stop delivering data **without the OS reporting an error**
(a macOS CDC quirk), so the host's reader doesn't see a drop. Symptom: the header shows
*host up — no device data* / *no new frames* while the device is actually alive (its `uptime`
keeps climbing after a reconnect).

The host now **self-heals**: a liveness watchdog forces a serial reconnect if no framed message
arrives for ~8 s, so it recovers in a few seconds (`watchdog: no device data … forcing reconnect`
in the log). You can also hit reconnect manually. If it recurs constantly, try a different
USB cable/port or a powered hub — the native-USB port is sensitive to marginal power.

Note: when the sniffer briefly goes deaf like this, it is *not* a device dropout, so the incident
detector deliberately **suppresses** the "silence" flood it would otherwise log for every device.

## Satellite crash-loops at boot: "Detected size(4096k) smaller than ... image header(8192k)"

```
E spi_flash: Detected size(4096k) smaller than the size in the binary image header(8192k). Probe failed.
assert failed: __esp_system_init_fn_init_flash startup_funcs.c:95 (flash_ret == ESP_OK)
```

The flashed image declares **8 MB** flash (the primary DevKitC-1's size) but the satellite chip
(ESP32-C6 SuperMini, ESP32-C6FH4) only has **4 MB**. Fixed in firmware ≥ 0.29 — reflash the
satellite with a current build (`pio run -e satellite -t upload`) and this goes away. See
[firmware.md](firmware.md#platformio-sdkconfig-pitfalls-important) for the root cause (PlatformIO
bakes the image-header flash size from `board_upload.flash_size`, independent of any sdkconfig
setting — it is not "auto-detected" at flash time as an earlier version of these docs claimed).

## Satellite wiring: GPIO10/GPIO11 have no pad on common "SuperMini" boards

If you wired the satellite's CS/SYNC to GPIO10/GPIO11 per an older version of
[the wiring diagram](tethered-to-satellite-wiring.md) and can't find a pad for them — you're not
missing anything. On the common ESP32-C6 SuperMini (e.g. MakerGO), **GPIO10 and GPIO11 are not
broken out at all** (only GPIO0–9 and GPIO12–23 have pads). Firmware ≥ 0.29 moved satellite
CS/SYNC to **GPIO4/GPIO5**, which are confirmed broken out — reflash and rewire to the current
diagram. If your specific board variant differs, check its silkscreen and update
`firmware/src/satellite/transport_spi.h` to match.

## No frames captured

1. **Wrong channel.** Pin to your HA channel (`--channel`); find it in ZHA Network settings or
   Z2M → Settings → Network. See [channels-reference.md](channels-reference.md).
2. **Nothing transmitting.** Quiet networks are quiet — poke a device (toggle a light) and watch
   for frames. `STATUS` should still tick ~1 Hz even with no frames.
3. **Wrong serial port.** macOS `/dev/cu.usbmodem*`, Linux `/dev/ttyACM*`, Windows `COMx`.
4. **Console corrupting the stream.** Only the `usb-sniffer` build routes console to UART0; if you
   flashed a different build, ESP_LOG may interleave with the binary stream.

## Wireshark shows malformed frames / bad FCS

The radio may or may not deliver the 2-byte FCS. If Wireshark flags FCS errors on every frame,
add `--no-fcs` to `pcap_bridge` (sets the TAP FCS-type TLV to "none"). See the FCS note in
[firmware.md](firmware.md).

## Wireshark shows encrypted payloads

Expected. Add your network key in Wireshark (Preferences → Protocols → ZigBee → Pre-configured
Keys) or use the host decryptor (Block C). See
[decoding-and-decryption.md](decoding-and-decryption.md).

## Dropped frames (`dropped_buf` rising in STATUS)

The capture queue overflowed — the host wasn't draining fast enough, or the link is saturated.
Mostly a concern on very busy networks or in `standalone` mode (WiFi contends for the radio).
Prefer `usb-sniffer` for high-fidelity capture; consider a [satellite radio](multi-radio.md).

## Standalone mode capture looks worse than USB

Expected — WiFi and 802.15.4 share the antenna in standalone mode. See the coexistence note in
[hardware.md](hardware.md). Use USB/host mode for the most complete capture.

## Network-wide dropouts / nothing will pair — check for a loud neighbour

If the *whole* Zigbee network degrades at once (outbound commands fail with `NWK_NO_ROUTE`,
devices unreachable, pairing associates but never completes) while inbound reports still trickle
in, suspect a **strong nearby transmitter** before debugging individual devices — another Zigbee
hub (Hue bridge), a Thread border router, or a busy 2.4 GHz source physically close to your
coordinator. It does **not** have to share your channel: at short range it blocks the
coordinator's receiver regardless (front-end desense).

1. Open **RF spectrum → Channel airtime**. A foreign PAN with a high frame rate *and* an RSSI
   histogram piled at the loud end (≥ −60 dBm at a sniffer placed near the coordinator) is your
   suspect.
2. Confirm by separation: move the suspect hub (or your coordinator) a few metres and watch the
   failure stop. Unplugging the suspect is the blunt version of the same test.
3. Keep them apart permanently, put the other hub on a distant channel anyway
   ([channels-reference.md](channels-reference.md)), and get the coordinator off USB3 ports onto
   a short USB 2.0 extension.

## Host tests

```bash
cd host && . .venv/bin/activate && python -m pytest -q
```
If imports fail, ensure you installed the package (`pip install -e .`) inside the venv.
