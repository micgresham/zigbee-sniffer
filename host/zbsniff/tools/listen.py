"""Human-readable console monitor — quick sanity check that a dongle is alive.

    python -m zbsniff.tools.listen --port /dev/ttyACM0 --channel 15
"""

from __future__ import annotations

import argparse
import time

from .. import proto
from ..transport.serial_reader import SerialReader


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="Console monitor for the sniffer")
    ap.add_argument("--port", action="append", required=True)
    ap.add_argument("--baud", type=int, default=921600)
    ap.add_argument("--channel", type=int, default=None)
    ap.add_argument("--ed", action="store_true", help="run an energy-detect sweep instead")
    args = ap.parse_args(argv)

    reader = SerialReader(args.port, baud=args.baud)
    reader.start()
    time.sleep(0.3)

    if args.channel is not None:
        reader.send(proto.cmd_set_channel(args.channel))
    if args.ed:
        reader.send(proto.cmd_set_mode(proto.Mode.ED_SWEEP))
    else:
        reader.send(proto.cmd_set_mode(proto.Mode.CAPTURE))
    reader.send(proto.cmd_start())

    try:
        for t in reader:
            m = t.message
            if isinstance(m, proto.CapturedFrame):
                print(f"[{t.port}] FRAME ch={m.channel:2d} rssi={m.rssi:4d}dBm "
                      f"lqi={m.lqi:3d} len={len(m.mpdu):3d} "
                      f"{m.mpdu[:16].hex(' ')}{'...' if len(m.mpdu) > 16 else ''}")
            elif isinstance(m, proto.EdResult):
                bars = "#" * max(0, (m.ed_dbm + 100) // 3)
                print(f"[{t.port}] ED   ch={m.channel:2d} {m.ed_dbm:4d}dBm {bars}")
            elif isinstance(m, proto.Status):
                print(f"[{t.port}] STATUS ch={m.channel} mode={m.mode} "
                      f"captured={m.captured} dropped_buf={m.dropped_buf} "
                      f"up={m.uptime_s}s fw={m.fw_major}.{m.fw_minor}")
            elif isinstance(m, proto.LogLine):
                print(f"[{t.port}] LOG  {m.text}")
    except KeyboardInterrupt:
        pass
    finally:
        reader.stop()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
