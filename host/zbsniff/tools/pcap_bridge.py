"""Stream captured frames from sniffer dongle(s) to a pcap file or to Wireshark.

Examples
--------
Live into Wireshark:
    python -m zbsniff.tools.pcap_bridge --port /dev/ttyACM0 | wireshark -k -i -

To a file on a fixed channel:
    python -m zbsniff.tools.pcap_bridge --port /dev/ttyACM0 --channel 15 --out cap.pcap

Multiple radios (USB hub / multiple dongles), merged into one capture:
    python -m zbsniff.tools.pcap_bridge --port /dev/ttyACM0 --port /dev/ttyACM1 --out cap.pcap
"""

from __future__ import annotations

import argparse
import sys
import time

from .. import proto
from ..store.pcap import PcapWriter
from ..transport.serial_reader import SerialReader


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="Zigbee sniffer -> pcap/Wireshark bridge")
    ap.add_argument("--port", action="append", required=True,
                    help="serial port (repeatable for multiple radios)")
    ap.add_argument("--baud", type=int, default=921600)
    ap.add_argument("--channel", type=int, default=None,
                    help="set capture channel 11-26 on start")
    ap.add_argument("--out", default="-",
                    help="output pcap file, or '-' for stdout (default)")
    ap.add_argument("--device-time", action="store_true",
                    help="use device timestamps instead of host arrival time")
    ap.add_argument("--no-fcs", action="store_true",
                    help="frames do NOT include the 2-byte FCS")
    args = ap.parse_args(argv)

    out_fp = sys.stdout.buffer if args.out == "-" else open(args.out, "wb")
    writer = PcapWriter(out_fp, fcs_present=not args.no_fcs)

    reader = SerialReader(args.port, baud=args.baud)
    reader.start()
    time.sleep(0.3)

    if args.channel is not None:
        reader.send(proto.cmd_set_channel(args.channel))
    reader.send(proto.cmd_set_mode(proto.Mode.CAPTURE))
    reader.send(proto.cmd_start())

    eprint = lambda *a: print(*a, file=sys.stderr, flush=True)  # noqa: E731
    eprint(f"[pcap-bridge] capturing on {args.port} -> {args.out}")

    try:
        for tagged in reader:
            if not isinstance(tagged.message, proto.CapturedFrame):
                continue
            f = tagged.message
            ts_us = f.timestamp if args.device_time else int(tagged.host_ts * 1_000_000)
            writer.write_frame(ts_us, f.rssi, f.lqi, f.channel, f.mpdu)
    except KeyboardInterrupt:
        eprint("\n[pcap-bridge] stopping")
    finally:
        reader.stop()
        if out_fp is not sys.stdout.buffer:
            out_fp.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
