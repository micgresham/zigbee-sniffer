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

## Host tests

```bash
cd host && . .venv/bin/activate && python -m pytest -q
```
If imports fail, ensure you installed the package (`pip install -e .`) inside the venv.
