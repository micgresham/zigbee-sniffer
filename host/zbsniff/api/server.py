"""Host app entrypoint: wire serial ingest + DB + API, run with uvicorn.

    python -m zbsniff.api.server --port /dev/ttyACM0 --db zbsniff.sqlite --channel 15
    # or, as the HA add-on, configured via env (see addon/run.sh)
"""

from __future__ import annotations

import argparse
import os

from ..store.db import Database
from ..transport.serial_reader import SerialReader
from .. import proto
from .app import EventHub, create_app
from .pipeline import IngestService


def build(args) -> "tuple":
    db = Database(args.db)
    hub = EventHub()
    key = bytes.fromhex(args.key) if args.key else None
    ingest = IngestService(db, hub, network_key=key)

    # Optional coordinator integration for name resolution.
    registry = None
    if args.z2m_host or args.zha_url:
        from ..integrations import DeviceRegistry
        registry = DeviceRegistry()
        _start_integrations(registry, args)

    app = create_app(db, hub, registry=registry)

    reader = None
    if args.port:
        reader = SerialReader(args.port, baud=args.baud)
        reader.start()
        if args.channel:
            reader.send(proto.cmd_set_channel(args.channel))
        reader.send(proto.cmd_set_mode(proto.Mode.CAPTURE))
        reader.send(proto.cmd_start())
        ingest.run_in_thread(reader)
    return app, db, reader, ingest


def _start_integrations(registry, args) -> None:
    """Run the configured Z2M/ZHA client(s) in a background asyncio thread."""
    import asyncio
    import threading

    async def runner():
        tasks = []
        if args.z2m_host:
            from ..integrations.z2m_mqtt import Z2MClient
            tasks.append(Z2MClient(registry, args.z2m_host, args.z2m_port).run())
        if args.zha_url and args.zha_token:
            from ..integrations.zha_ws import ZHAClient
            tasks.append(ZHAClient(registry, args.zha_url, args.zha_token).run())
        await asyncio.gather(*tasks, return_exceptions=True)

    threading.Thread(target=lambda: asyncio.run(runner()), daemon=True).start()


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="Zigbee sniffer host app")
    ap.add_argument("--port", action="append", help="serial port(s); omit to run API only")
    ap.add_argument("--baud", type=int, default=921600)
    ap.add_argument("--channel", type=int, default=int(os.environ.get("ZB_CHANNEL", "0")) or None)
    ap.add_argument("--db", default=os.environ.get("ZB_DB", "zbsniff.sqlite"))
    ap.add_argument("--key", default=os.environ.get("ZB_NETWORK_KEY"),
                    help="Zigbee network key as 32 hex chars (optional)")
    ap.add_argument("--host", default="0.0.0.0")
    ap.add_argument("--http-port", type=int, default=int(os.environ.get("ZB_HTTP_PORT", "8080")))
    # Optional coordinator integrations
    ap.add_argument("--z2m-host", default=os.environ.get("ZB_Z2M_HOST"))
    ap.add_argument("--z2m-port", type=int, default=int(os.environ.get("ZB_Z2M_PORT", "1883")))
    ap.add_argument("--zha-url", default=os.environ.get("ZB_ZHA_URL"))
    ap.add_argument("--zha-token", default=os.environ.get("ZB_ZHA_TOKEN"))
    args = ap.parse_args(argv)

    import uvicorn
    app, _db, _reader, _ingest = build(args)
    uvicorn.run(app, host=args.host, port=args.http_port)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
