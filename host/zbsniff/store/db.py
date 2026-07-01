"""SQLite persistence + lightweight analytics for the host app.

Stores packets, a device registry, link-quality stats, energy-detect samples,
and incidents. Device/link aggregation happens on ingest, so the API can serve
the device list, routing graph, spectrum, and incident history directly.

Uses the stdlib `sqlite3` (no extra dependency).
"""

from __future__ import annotations

import json
import sqlite3
import time
from typing import Any

SCHEMA = """
CREATE TABLE IF NOT EXISTS packets (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts REAL, radio INTEGER, channel INTEGER, rssi INTEGER, lqi INTEGER,
    src INTEGER, dst INTEGER, ftype TEXT, summary TEXT, decrypted INTEGER
);
CREATE INDEX IF NOT EXISTS idx_packets_src ON packets(src);
CREATE INDEX IF NOT EXISTS idx_packets_ts ON packets(ts);

CREATE TABLE IF NOT EXISTS devices (
    addr INTEGER PRIMARY KEY,
    ext INTEGER, role TEXT, name TEXT,
    first_seen REAL, last_seen REAL, count INTEGER,
    last_rssi INTEGER, last_lqi INTEGER, last_channel INTEGER
);

CREATE TABLE IF NOT EXISTS links (
    src INTEGER, dst INTEGER, count INTEGER,
    last_lqi INTEGER, last_rssi INTEGER, last_seen REAL,
    PRIMARY KEY (src, dst)
);

CREATE TABLE IF NOT EXISTS ed_samples (
    ts REAL, channel INTEGER, ed_dbm INTEGER, sweep_id INTEGER, radio INTEGER
);
CREATE INDEX IF NOT EXISTS idx_ed_ts ON ed_samples(ts);

CREATE TABLE IF NOT EXISTS incidents (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts REAL, addr TEXT, reason TEXT, rssi INTEGER, lqi INTEGER,
    channel INTEGER, ed INTEGER, silent_s INTEGER, raw TEXT
);
"""

BROADCAST = 0xFFFF


def _hexaddr(a: int | None) -> str | None:
    return None if a is None else f"0x{a:04x}"


class Database:
    def __init__(self, path: str = ":memory:"):
        self.conn = sqlite3.connect(path, check_same_thread=False)
        self.conn.row_factory = sqlite3.Row
        self.conn.executescript(SCHEMA)
        self.conn.commit()

    # --- ingest ------------------------------------------------------------
    def ingest_frame(self, ts: float, radio: int, channel: int, rssi: int, lqi: int,
                     decoded) -> None:
        mac = decoded.mac
        # MacFrame.src_addr/dst_addr are ints (16- or 64-bit) or None.
        src_i = mac.src_addr
        dst_i = mac.dst_addr
        decrypted = 1 if (decoded.nwk and decoded.nwk.decrypted) else 0

        self.conn.execute(
            "INSERT INTO packets(ts,radio,channel,rssi,lqi,src,dst,ftype,summary,decrypted)"
            " VALUES(?,?,?,?,?,?,?,?,?,?)",
            (ts, radio, channel, rssi, lqi, src_i, dst_i, mac.type_name,
             decoded.summary, decrypted))

        if src_i is not None and src_i != BROADCAST:
            self._upsert_device(src_i, ts, rssi, lqi, channel,
                                ext=(decoded.nwk.src_ieee if decoded.nwk else None))
        if src_i is not None and dst_i is not None and dst_i != BROADCAST:
            self._upsert_link(src_i, dst_i, lqi, rssi, ts)
        self.conn.commit()

    def _upsert_device(self, addr, ts, rssi, lqi, channel, ext=None):
        cur = self.conn.execute("SELECT count FROM devices WHERE addr=?", (addr,))
        row = cur.fetchone()
        role = "coordinator" if addr == 0 else None
        if row:
            self.conn.execute(
                "UPDATE devices SET last_seen=?,count=count+1,last_rssi=?,last_lqi=?,"
                "last_channel=?,ext=COALESCE(?,ext) WHERE addr=?",
                (ts, rssi, lqi, channel, ext, addr))
        else:
            self.conn.execute(
                "INSERT INTO devices(addr,ext,role,first_seen,last_seen,count,"
                "last_rssi,last_lqi,last_channel) VALUES(?,?,?,?,?,1,?,?,?)",
                (addr, ext, role, ts, ts, rssi, lqi, channel))

    def _upsert_link(self, src, dst, lqi, rssi, ts):
        self.conn.execute(
            "INSERT INTO links(src,dst,count,last_lqi,last_rssi,last_seen) VALUES(?,?,1,?,?,?)"
            " ON CONFLICT(src,dst) DO UPDATE SET count=count+1,last_lqi=?,last_rssi=?,last_seen=?",
            (src, dst, lqi, rssi, ts, lqi, rssi, ts))

    def ingest_ed(self, ts, channel, ed_dbm, sweep_id, radio):
        self.conn.execute(
            "INSERT INTO ed_samples(ts,channel,ed_dbm,sweep_id,radio) VALUES(?,?,?,?,?)",
            (ts, channel, ed_dbm, sweep_id, radio))
        self.conn.commit()

    def ingest_incident(self, inc: dict):
        self.conn.execute(
            "INSERT INTO incidents(ts,addr,reason,rssi,lqi,channel,ed,silent_s,raw)"
            " VALUES(?,?,?,?,?,?,?,?,?)",
            (inc.get("ts"), inc.get("addr"), inc.get("reason"), inc.get("rssi"),
             inc.get("lqi"), inc.get("ch"), inc.get("ed"), inc.get("silent_s"),
             json.dumps(inc)))
        self.conn.commit()

    # --- queries -----------------------------------------------------------
    def devices(self) -> list[dict[str, Any]]:
        rows = self.conn.execute(
            "SELECT * FROM devices ORDER BY last_seen DESC").fetchall()
        out = []
        for r in rows:
            d = dict(r)
            d["addr"] = _hexaddr(r["addr"])
            d["ext"] = None if r["ext"] is None else f"0x{r['ext']:016x}"
            out.append(d)
        return out

    def routing(self) -> dict[str, list]:
        devs = self.devices()
        edges = []
        for r in self.conn.execute("SELECT * FROM links ORDER BY count DESC").fetchall():
            edges.append({"src": _hexaddr(r["src"]), "dst": _hexaddr(r["dst"]),
                          "lqi": r["last_lqi"], "rssi": r["last_rssi"], "count": r["count"]})
        return {"nodes": devs, "edges": edges}

    def messages(self, addr: int, limit: int = 50) -> list[dict]:
        rows = self.conn.execute(
            "SELECT * FROM packets WHERE src=? OR dst=? ORDER BY ts DESC LIMIT ?",
            (addr, addr, limit)).fetchall()
        return [dict(r) for r in rows]

    def spectrum(self, since: float | None = None) -> list[dict]:
        if since is None:
            since = time.time() - 300
        rows = self.conn.execute(
            "SELECT ts,channel,ed_dbm,sweep_id FROM ed_samples WHERE ts>=? ORDER BY ts",
            (since,)).fetchall()
        return [dict(r) for r in rows]

    def incidents(self, limit: int = 100) -> list[dict]:
        rows = self.conn.execute(
            "SELECT * FROM incidents ORDER BY id DESC LIMIT ?", (limit,)).fetchall()
        return [dict(r) for r in rows]

    def close(self):
        self.conn.close()


def _to_int(addr) -> int | None:
    if addr is None:
        return None
    if isinstance(addr, int):
        return addr
    try:
        return int(addr, 16) if isinstance(addr, str) else int(addr)
    except (ValueError, TypeError):
        return None
