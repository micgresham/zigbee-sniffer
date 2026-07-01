import time

from zbsniff.decode import decode_frame
from zbsniff.store.db import Database


def _mac_data_frame():
    # FCF 0x8861 (Data, dst16+src16, PAN compression), src 0x5678 -> dst 0x1234
    return bytes([0x61, 0x88, 0x2a, 0xcd, 0xab, 0x34, 0x12, 0x78, 0x56,
                  0x09, 0x00,            # tiny NWK-ish payload (unsecured, ignored)
                  0x00, 0x00])           # FCS


def test_ingest_device_and_link():
    db = Database()
    dec = decode_frame(_mac_data_frame())
    db.ingest_frame(time.time(), radio=0, channel=15, rssi=-61, lqi=200, decoded=dec)

    devs = db.devices()
    addrs = {d["addr"] for d in devs}
    assert "0x5678" in addrs
    src = next(d for d in devs if d["addr"] == "0x5678")
    assert src["count"] == 1 and src["last_lqi"] == 200 and src["last_channel"] == 15

    routing = db.routing()
    assert any(e["src"] == "0x5678" and e["dst"] == "0x1234" for e in routing["edges"])


def test_link_counts_increment():
    db = Database()
    dec = decode_frame(_mac_data_frame())
    for _ in range(3):
        db.ingest_frame(time.time(), 0, 15, -61, 200, dec)
    edge = db.routing()["edges"][0]
    assert edge["count"] == 3


def test_ed_and_incidents():
    db = Database()
    now = time.time()
    db.ingest_ed(now, channel=20, ed_dbm=-85, sweep_id=1, radio=0)
    assert db.spectrum(since=now - 1)[0]["channel"] == 20

    db.ingest_incident({"ts": 120, "addr": "0x1234", "reason": "silence",
                        "rssi": -72, "lqi": 110, "ch": 15, "ed": -88, "silent_s": 130})
    incs = db.incidents()
    assert incs[0]["addr"] == "0x1234" and incs[0]["silent_s"] == 130


def test_coordinator_role():
    db = Database()
    # FCF data, src 0x0000 (coordinator) -> dst 0x1234
    mpdu = bytes([0x61, 0x88, 0x01, 0xcd, 0xab, 0x34, 0x12, 0x00, 0x00, 0x09, 0x00, 0x00, 0x00])
    db.ingest_frame(time.time(), 0, 15, -40, 255, decode_frame(mpdu))
    coord = next(d for d in db.devices() if d["addr"] == "0x0000")
    assert coord["role"] == "coordinator"
