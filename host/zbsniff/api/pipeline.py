"""Ingest pipeline: serial stream → decode → SQLite → live WebSocket.

`process()` handles a single parsed message and is synchronous/testable without
hardware; `run()` drives it from a SerialReader in a background thread.
"""

from __future__ import annotations

import threading
import time

from .. import proto
from ..decode import decode_frame
from ..store.db import Database
from .app import EventHub


class IngestService:
    def __init__(self, db: Database, hub: EventHub, network_key: bytes | None = None):
        self.db = db
        self.hub = hub
        self.network_key = network_key
        self._stop = threading.Event()

    def process(self, tagged) -> dict | None:
        """Decode + store one tagged message; return the event published (or None)."""
        m = tagged.message
        ts = tagged.host_ts

        if isinstance(m, proto.CapturedFrame):
            decoded = decode_frame(m.mpdu, self.network_key)
            self.db.ingest_frame(ts, m.radio_id, m.channel, m.rssi, m.lqi, decoded)
            event = {
                "kind": "frame", "ts": ts, "radio": m.radio_id, "channel": m.channel,
                "rssi": m.rssi, "lqi": m.lqi,
                "src": decoded.mac.src_addr, "dst": decoded.mac.dst_addr,
                "type": decoded.mac.type_name, "summary": decoded.summary,
                "decrypted": bool(decoded.nwk and decoded.nwk.decrypted),
            }
        elif isinstance(m, proto.EdResult):
            self.db.ingest_ed(ts, m.channel, m.ed_dbm, m.sweep_id, m.radio_id)
            event = {"kind": "ed", "ts": ts, "channel": m.channel,
                     "ed_dbm": m.ed_dbm, "sweep_id": m.sweep_id}
        elif isinstance(m, proto.Incident):
            self.db.ingest_incident(m.data)
            event = {"kind": "incident", **m.data}
        elif isinstance(m, proto.Status):
            event = {"kind": "status", "channel": m.channel, "captured": m.captured,
                     "dropped_buf": m.dropped_buf, "radio": m.radio_id}
        else:
            return None

        self.hub.publish_threadsafe(event)
        return event

    def run(self, reader) -> None:
        for tagged in reader:
            if self._stop.is_set():
                break
            try:
                self.process(tagged)
            except Exception:  # never let one bad frame kill ingest
                pass

    def run_in_thread(self, reader) -> threading.Thread:
        t = threading.Thread(target=self.run, args=(reader,), daemon=True)
        t.start()
        return t

    def stop(self) -> None:
        self._stop.set()
