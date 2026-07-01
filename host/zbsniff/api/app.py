"""FastAPI app: REST over the SQLite store + a live WebSocket + static frontend.

`create_app(db)` is dependency-injected with a Database so it's testable with
FastAPI's TestClient against a pre-populated store (no hardware needed). Live
data is published through an asyncio event hub that the ingest pipeline feeds.
"""

from __future__ import annotations

import asyncio
import os

from ..store.db import Database, _to_int


class EventHub:
    """Fan-out of live events (frames/ed/incidents) to WebSocket subscribers."""

    def __init__(self) -> None:
        self._subs: set[asyncio.Queue] = set()
        self.loop: asyncio.AbstractEventLoop | None = None

    def subscribe(self) -> asyncio.Queue:
        q: asyncio.Queue = asyncio.Queue(maxsize=1000)
        self._subs.add(q)
        return q

    def unsubscribe(self, q: asyncio.Queue) -> None:
        self._subs.discard(q)

    def publish(self, event: dict) -> None:
        for q in list(self._subs):
            try:
                q.put_nowait(event)
            except asyncio.QueueFull:
                pass

    def publish_threadsafe(self, event: dict) -> None:
        """Publish from a non-async thread (the serial ingest thread)."""
        if self.loop is not None:
            self.loop.call_soon_threadsafe(self.publish, event)


def create_app(db: Database, hub: EventHub | None = None, frontend_dir: str | None = None,
               registry=None):
    from fastapi import FastAPI, WebSocket, WebSocketDisconnect
    from fastapi.responses import JSONResponse

    hub = hub or EventHub()
    app = FastAPI(title="Zigbee Sniffer", version="0.1.0")
    app.state.hub = hub
    app.state.db = db
    app.state.registry = registry  # optional Z2M/ZHA DeviceRegistry for name resolution
    app.state.config = {"channel": 15, "decrypt": False, "ha_integration": "none"}

    def _name(addr_hex):
        """Resolve a '0x1234' address to a coordinator friendly name, if known."""
        if not registry or not addr_hex:
            return None
        try:
            return registry.name_for(short=int(addr_hex, 16))
        except (ValueError, TypeError):
            return None

    @app.on_event("startup")
    async def _capture_loop():
        hub.loop = asyncio.get_running_loop()

    @app.get("/api/devices")
    def devices():
        out = db.devices()
        for d in out:
            d["name"] = d.get("name") or _name(d.get("addr"))
        return out

    @app.get("/api/routing")
    def routing():
        r = db.routing()
        for n in r["nodes"]:
            n["name"] = n.get("name") or _name(n.get("addr"))
        return r

    @app.get("/api/devices/{addr}/messages")
    def messages(addr: str, limit: int = 50):
        ai = _to_int(addr)
        if ai is None:
            return JSONResponse({"error": "bad address"}, status_code=400)
        return db.messages(ai, limit)

    @app.get("/api/spectrum")
    def spectrum(since: float | None = None):
        return db.spectrum(since)

    @app.get("/api/incidents")
    def incidents(limit: int = 100):
        return db.incidents(limit)

    @app.get("/api/config")
    def get_config():
        return app.state.config

    @app.put("/api/config")
    def put_config(cfg: dict):
        app.state.config.update(cfg)
        return app.state.config

    @app.get("/api/health")
    def health():
        return {"status": "ok", "devices": len(db.devices())}

    @app.websocket("/ws")
    async def ws(sock: WebSocket):
        await sock.accept()
        q = hub.subscribe()
        try:
            while True:
                event = await q.get()
                await sock.send_json(event)
        except WebSocketDisconnect:
            pass
        finally:
            hub.unsubscribe(q)

    # Serve the built React UI if present (frontend/dist).
    fe = frontend_dir or os.path.join(os.path.dirname(__file__), "..", "..", "..",
                                      "frontend", "dist")
    if os.path.isdir(fe):
        from fastapi.staticfiles import StaticFiles
        app.mount("/", StaticFiles(directory=fe, html=True), name="frontend")

    return app
