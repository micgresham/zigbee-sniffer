# Getting started

> **Current status: Block A** — the USB true-sniffer path works end-to-end (firmware → host →
> Wireshark). The standalone web UI and full host app land in Blocks B–C.

## Prerequisites

- **VS Code** + the **PlatformIO IDE** extension (or the `pio` CLI).
- **Python 3.10+** for the host tools.
- An **ESP32-C6** board (DevKitC-1 recommended) and a data-capable USB-C cable.
- **Wireshark** (optional, for live frame inspection).

## 1. Flash the USB sniffer firmware

```bash
cd firmware
pio run -e tethered -t upload
```

The first build downloads the ESP-IDF toolchain (a few minutes). On success the board
enumerates as a USB serial device (`/dev/ttyACM0` on Linux, `/dev/cu.usbmodemXXXX` on macOS,
`COMx` on Windows).

> The console log is routed to **UART0** in this build so it doesn't corrupt the binary USB
> stream — that's intentional (see [firmware.md](firmware.md)).

## 2. Install the host tools

```bash
cd host
python3 -m venv .venv && . .venv/bin/activate
pip install -e .
```

## 3. Sanity check — console monitor

```bash
python -m zbsniff.tools.listen --port /dev/ttyACM0 --channel 15
```
You should see `FRAME` lines (and a periodic `STATUS`). If your HA network is on a different
channel, set it with `--channel`. (Find your channel in HA: ZHA → Network settings, or Z2M →
Settings → Network.) See [channels-reference.md](channels-reference.md).

## 4. Capture into Wireshark

Live:
```bash
python -m zbsniff.tools.pcap_bridge --port /dev/ttyACM0 --channel 15 | wireshark -k -i -
```
To a file:
```bash
python -m zbsniff.tools.pcap_bridge --port /dev/ttyACM0 --channel 15 --out capture.pcap
```
Frames carry RSSI, LQI, and channel via the IEEE 802.15.4 **TAP** link type, so Wireshark
dissects them natively. Encrypted payloads stay encrypted unless you add your network key in
Wireshark (or use the host app's decryption — Block C). See
[decoding-and-decryption.md](decoding-and-decryption.md).

## 5. Multiple radios

Plug in more dongles and pass `--port` repeatedly — the host merges the streams:
```bash
python -m zbsniff.tools.pcap_bridge --port /dev/ttyACM0 --port /dev/ttyACM1 --out capture.pcap
```
For a tidy permanent rig, see the [carrier board](carrier.md).

## Standalone mode (Block B — in progress)

```bash
pio run -e standalone -t upload
```
Then join the `zb-sniffer` Wi-Fi AP and browse to `http://192.168.4.1`. The current build is a
scaffold; the diagnostic UI is being built in Block B.

## Troubleshooting

See [troubleshooting.md](troubleshooting.md).
