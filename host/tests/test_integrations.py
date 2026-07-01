import time

import pytest

from zbsniff import proto
from zbsniff.integrations import (DeviceRegistry, parse_z2m_devices, parse_zha_devices)
from zbsniff.store.db import Database
from zbsniff.api.app import EventHub, create_app
from zbsniff.api.pipeline import IngestService
from zbsniff.transport.types import Tagged


def test_parse_z2m_devices():
    payload = [
        {"ieee_address": "0x00124b0001020304", "network_address": 0x1234,
         "friendly_name": "kitchen_light", "type": "Router"},
        {"ieee_address": "0x00124b0009080706", "network_address": 0,
         "friendly_name": "Coordinator", "type": "Coordinator"},
    ]
    infos = parse_z2m_devices(payload)
    assert infos[0].short == 0x1234
    assert infos[0].name == "kitchen_light"
    assert infos[0].type == "router"
    assert infos[1].type == "coordinator"


def test_parse_zha_devices():
    payload = [
        {"ieee": "00:12:4b:00:01:02:03:04", "nwk": "0x1234",
         "name": "sensor", "user_given_name": "Back Door", "device_type": "EndDevice"},
    ]
    infos = parse_zha_devices(payload)
    assert infos[0].short == 0x1234
    assert infos[0].ieee == 0x00124B0001020304
    assert infos[0].name == "Back Door"      # user_given_name preferred
    assert infos[0].type == "end_device"


def test_registry_resolve():
    reg = DeviceRegistry()
    reg.update(parse_z2m_devices([
        {"ieee_address": "0x00124b0001020304", "network_address": 0x1234,
         "friendly_name": "kitchen_light", "type": "Router"}]))
    assert reg.name_for(short=0x1234) == "kitchen_light"
    assert reg.name_for(ieee=0x00124B0001020304) == "kitchen_light"
    assert reg.name_for(short=0x9999) is None


def test_api_name_resolution():
    pytest.importorskip("fastapi")
    pytest.importorskip("httpx")
    from fastapi.testclient import TestClient

    db = Database()
    hub = EventHub()
    reg = DeviceRegistry()
    reg.update(parse_z2m_devices([
        {"ieee_address": "0x00124b0001020304", "network_address": 0x5678,
         "friendly_name": "kitchen_light", "type": "Router"}]))

    # ingest a frame from 0x5678
    ingest = IngestService(db, hub)
    cf = proto.CapturedFrame(0, 15, -61, 200, proto.FLAG_CRC_OK, 1,
                             bytes([0x61, 0x88, 0x2a, 0xcd, 0xab, 0x34, 0x12, 0x78, 0x56, 0x09, 0x00, 0, 0]))
    ingest.process(Tagged("p", time.time(), proto.Msg.CAPTURED_FRAME, cf))

    client = TestClient(create_app(db, hub, registry=reg))
    devs = client.get("/api/devices").json()
    kitchen = next(d for d in devs if d["addr"] == "0x5678")
    assert kitchen["name"] == "kitchen_light"
