import time

import pytest

from zbsniff import proto
from zbsniff.store.db import Database
from zbsniff.api.app import EventHub, create_app
from zbsniff.api.pipeline import IngestService


def _captured():
    return proto.CapturedFrame(
        radio_id=0, channel=15, rssi=-61, lqi=200, flags=proto.FLAG_CRC_OK,
        timestamp=1,
        mpdu=bytes([0x61, 0x88, 0x2a, 0xcd, 0xab, 0x34, 0x12, 0x78, 0x56, 0x09, 0x00, 0, 0]))


def test_pipeline_process_stores_and_returns_event():
    from zbsniff.transport.types import Tagged
    db = Database()
    ingest = IngestService(db, EventHub())
    ev = ingest.process(Tagged("p0", time.time(), proto.Msg.CAPTURED_FRAME, _captured()))
    assert ev["kind"] == "frame"
    assert ev["src"] == 0x5678 and ev["dst"] == 0x1234
    assert any(d["addr"] == "0x5678" for d in db.devices())


def test_pipeline_incident():
    from zbsniff.transport.types import Tagged
    db = Database()
    ingest = IngestService(db, EventHub())
    inc = proto.Incident('{"ts":120,"addr":"0x1234","reason":"silence","silent_s":130}',
                         {"ts": 120, "addr": "0x1234", "reason": "silence", "silent_s": 130})
    ev = ingest.process(Tagged("p0", time.time(), proto.Msg.INCIDENT, inc))
    assert ev["kind"] == "incident"
    assert db.incidents()[0]["addr"] == "0x1234"


def test_rest_endpoints():
    pytest.importorskip("fastapi")
    pytest.importorskip("httpx")
    from fastapi.testclient import TestClient
    from zbsniff.transport.types import Tagged

    db = Database()
    hub = EventHub()
    ingest = IngestService(db, hub)
    ingest.process(Tagged("p0", time.time(), proto.Msg.CAPTURED_FRAME, _captured()))

    client = TestClient(create_app(db, hub))
    assert client.get("/api/health").json()["status"] == "ok"
    devices = client.get("/api/devices").json()
    assert any(d["addr"] == "0x5678" for d in devices)
    routing = client.get("/api/routing").json()
    assert routing["edges"]
    assert client.get("/api/incidents").json() == []
    cfg = client.put("/api/config", json={"channel": 20}).json()
    assert cfg["channel"] == 20
