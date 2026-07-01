// Package store persists packets, devices, links, ED samples, and incidents in
// SQLite (pure-Go driver, no cgo). Port of host/zbsniff/store/db.py.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"zbsniff/internal/decode"
)

const broadcast = 0xFFFF

const schema = `
CREATE TABLE IF NOT EXISTS packets(
 id INTEGER PRIMARY KEY AUTOINCREMENT, ts REAL, radio INT, channel INT,
 rssi INT, lqi INT, src INT, dst INT, ftype TEXT, summary TEXT, decrypted INT);
CREATE INDEX IF NOT EXISTS idx_packets_src ON packets(src);
CREATE TABLE IF NOT EXISTS devices(
 addr INTEGER PRIMARY KEY, ext INT, role TEXT, name TEXT,
 first_seen REAL, last_seen REAL, count INT, last_rssi INT, last_lqi INT, last_channel INT);
CREATE TABLE IF NOT EXISTS links(
 src INT, dst INT, count INT, last_lqi INT, last_rssi INT, last_seen REAL,
 PRIMARY KEY(src,dst));
CREATE TABLE IF NOT EXISTS ed_samples(ts REAL, channel INT, ed_dbm INT, sweep_id INT, radio INT);
CREATE TABLE IF NOT EXISTS incidents(
 id INTEGER PRIMARY KEY AUTOINCREMENT, ts REAL, addr TEXT, reason TEXT,
 rssi INT, lqi INT, channel INT, ed INT, silent_s INT, raw TEXT);
CREATE TABLE IF NOT EXISTS route_failures(addr INTEGER PRIMARY KEY, count INT, last_reason TEXT, last_seen REAL);
CREATE TABLE IF NOT EXISTS probe_results(
 id INTEGER PRIMARY KEY AUTOINCREMENT, ts REAL, target INT, port TEXT, radio INT,
 acked INT, rssi INT, lqi INT);
CREATE INDEX IF NOT EXISTS idx_probe_target ON probe_results(target);
CREATE TABLE IF NOT EXISTS schedules(
 id INTEGER PRIMARY KEY AUTOINCREMENT, target INT, pan INT, port TEXT,
 interval_s INT, enabled INT, created REAL, last_run REAL, runs INT, acks INT);
`

// DB wraps the SQLite connection.
type DB struct {
	mu sync.Mutex
	db *sql.DB
}

// Open opens (or creates) the database at path (":memory:" for in-memory).
func Open(path string) (*DB, error) {
	d, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1) // serialize; simplest correct option for SQLite
	if _, err := d.Exec(schema); err != nil {
		return nil, err
	}
	return &DB{db: d}, nil
}

func hexAddr(a sql.NullInt64) string {
	if !a.Valid {
		return ""
	}
	return fmt.Sprintf("0x%04x", uint16(a.Int64))
}

// IngestFrame stores a decoded frame and updates device/link aggregates.
func (d *DB) IngestFrame(ts float64, radio, channel int, rssi, lqi int, dec *decode.Decoded) {
	d.mu.Lock()
	defer d.mu.Unlock()
	src := dec.MAC.SrcAddr
	dst := dec.MAC.DstAddr
	decrypted := 0
	if dec.NWK != nil && dec.NWK.Decrypted {
		decrypted = 1
	}
	var srcN, dstN any
	if src >= 0 {
		srcN = src
	}
	if dst >= 0 {
		dstN = dst
	}
	d.db.Exec(`INSERT INTO packets(ts,radio,channel,rssi,lqi,src,dst,ftype,summary,decrypted)
	           VALUES(?,?,?,?,?,?,?,?,?,?)`,
		ts, radio, channel, rssi, lqi, srcN, dstN, dec.MAC.TypeName, dec.Summary(), decrypted)

	// MAC-layer addresses are the physical hop (often router<->coordinator).
	if src >= 0 && src != broadcast {
		d.upsertDevice(int(src), ts, rssi, lqi, channel)
	}
	if src >= 0 && dst >= 0 && dst != broadcast {
		d.upsertLink(int(src), int(dst), lqi, rssi, ts)
	}

	// NWK-layer addresses are the actual source/destination DEVICES (the NWK
	// header is unencrypted, so this works even without the network key). This is
	// what reveals child devices that only ever talk through a router.
	if dec.NWK != nil {
		ns, nd := dec.NWK.Src, dec.NWK.Dst
		if ns >= 0 && ns < 0xFFF8 {
			d.upsertDevice(ns, ts, rssi, lqi, channel)
		}
		if ns >= 0 && ns < 0xFFF8 && nd >= 0 && nd < 0xFFF8 && ns != nd {
			d.upsertLink(ns, nd, lqi, rssi, ts)
		}
	}
}

func (d *DB) upsertLink(src, dst, lqi, rssi int, ts float64) {
	d.db.Exec(`INSERT INTO links(src,dst,count,last_lqi,last_rssi,last_seen) VALUES(?,?,1,?,?,?)
	           ON CONFLICT(src,dst) DO UPDATE SET count=count+1,last_lqi=?,last_rssi=?,last_seen=?`,
		src, dst, lqi, rssi, ts, lqi, rssi, ts)
}

func (d *DB) upsertDevice(addr int, ts float64, rssi, lqi, channel int) {
	var cnt int
	err := d.db.QueryRow(`SELECT count FROM devices WHERE addr=?`, addr).Scan(&cnt)
	role := any(nil)
	if addr == 0 {
		role = "coordinator"
	}
	if err == sql.ErrNoRows {
		d.db.Exec(`INSERT INTO devices(addr,role,first_seen,last_seen,count,last_rssi,last_lqi,last_channel)
		           VALUES(?,?,?,?,1,?,?,?)`, addr, role, ts, ts, rssi, lqi, channel)
	} else {
		d.db.Exec(`UPDATE devices SET last_seen=?,count=count+1,last_rssi=?,last_lqi=?,last_channel=? WHERE addr=?`,
			ts, rssi, lqi, channel, addr)
	}
}

// IngestED stores an energy-detect sample.
func (d *DB) IngestED(ts float64, channel, edDBm, sweepID, radio int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.db.Exec(`INSERT INTO ed_samples(ts,channel,ed_dbm,sweep_id,radio) VALUES(?,?,?,?,?)`,
		ts, channel, edDBm, sweepID, radio)
}

// IngestIncident stores an incident from the device JSON.
func (d *DB) IngestIncident(data map[string]any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	raw, _ := json.Marshal(data)
	get := func(k string) any { return data[k] }
	d.db.Exec(`INSERT INTO incidents(ts,addr,reason,rssi,lqi,channel,ed,silent_s,raw)
	           VALUES(?,?,?,?,?,?,?,?,?)`,
		get("ts"), get("addr"), get("reason"), get("rssi"), get("lqi"),
		get("ch"), get("ed"), get("silent_s"), string(raw))
}

// Devices returns the device registry as JSON-ready maps.
func (d *DB) Devices() []map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.db.Query(`SELECT addr,role,name,last_seen,count,last_rssi,last_lqi,last_channel
	                         FROM devices ORDER BY last_seen DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var addr int64
		var role, name sql.NullString
		var lastSeen sql.NullFloat64
		var count, rssi, lqi, ch sql.NullInt64
		rows.Scan(&addr, &role, &name, &lastSeen, &count, &rssi, &lqi, &ch)
		out = append(out, map[string]any{
			"addr": fmt.Sprintf("0x%04x", uint16(addr)), "role": role.String, "name": name.String,
			"count": count.Int64, "last_rssi": rssi.Int64, "last_lqi": lqi.Int64,
			"last_channel": ch.Int64,
		})
	}
	return out
}

// Routing returns nodes + edges for the routing view.
func (d *DB) Routing() map[string]any {
	devices := d.Devices()
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, _ := d.db.Query(`SELECT src,dst,count,last_lqi,last_rssi FROM links ORDER BY count DESC`)
	var edges []map[string]any
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var src, dst sql.NullInt64
			var count, lqi, rssi sql.NullInt64
			rows.Scan(&src, &dst, &count, &lqi, &rssi)
			edges = append(edges, map[string]any{
				"src": hexAddr(src), "dst": hexAddr(dst),
				"lqi": lqi.Int64, "rssi": rssi.Int64, "count": count.Int64,
			})
		}
	}
	return map[string]any{"nodes": devices, "edges": edges}
}

// Incidents returns the most recent incidents.
func (d *DB) Incidents(limit int) []map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, _ := d.db.Query(`SELECT ts,addr,reason,rssi,lqi,channel,ed,silent_s FROM incidents ORDER BY id DESC LIMIT ?`, limit)
	var out []map[string]any
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var ts sql.NullFloat64
			var addr, reason sql.NullString
			var rssi, lqi, ch, ed, silent sql.NullInt64
			rows.Scan(&ts, &addr, &reason, &rssi, &lqi, &ch, &ed, &silent)
			out = append(out, map[string]any{
				"ts": ts.Float64, "addr": addr.String, "reason": reason.String,
				"rssi": rssi.Int64, "lqi": lqi.Int64, "channel": ch.Int64,
				"ed": ed.Int64, "silent_s": silent.Int64,
			})
		}
	}
	return out
}

// IngestRouteFailure records an NWK route/link failure targeting a device.
func (d *DB) IngestRouteFailure(addr int, reason string, ts float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.db.Exec(`INSERT INTO route_failures(addr,count,last_reason,last_seen) VALUES(?,1,?,?)
	           ON CONFLICT(addr) DO UPDATE SET count=count+1,last_reason=?,last_seen=?`,
		addr, reason, ts, reason, ts)
}

// Diagnostics analyses the captured data and returns a list of health issues,
// each {severity, category, message, addr?}.
func (d *DB) Diagnostics() []map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []map[string]any
	add := func(sev, cat, msg string, addr int) {
		m := map[string]any{"severity": sev, "category": cat, "message": msg}
		if addr >= 0 {
			m["addr"] = fmt.Sprintf("0x%04x", uint16(addr))
		}
		out = append(out, m)
	}
	q := func(sql string, fn func(r *sql.Rows)) {
		rows, err := d.db.Query(sql)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			fn(rows)
		}
	}
	// Route failures (the strongest dropout signal).
	q(`SELECT addr,count,last_reason FROM route_failures WHERE count>=2 ORDER BY count DESC LIMIT 30`,
		func(r *sql.Rows) {
			var addr, c int
			var reason string
			r.Scan(&addr, &c, &reason)
			add("high", "Route failure", fmt.Sprintf("%d× \"%s\" — device hard to reach (re-pair or add a router nearby)", c, reason), addr)
		})
	// Weak links.
	q(`SELECT src,dst,last_lqi FROM links WHERE last_lqi < 50 AND count>=2 ORDER BY last_lqi ASC LIMIT 30`,
		func(r *sql.Rows) {
			var src, dst, lqi int
			r.Scan(&src, &dst, &lqi)
			sev := "warn"
			if lqi < 25 {
				sev = "high"
			}
			add(sev, "Weak link ⌖", fmt.Sprintf("0x%04x→0x%04x weak AS HEARD BY THE SNIFFER (LQI %d) — may be fine on the mesh; compare the device's Net LQI", uint16(src), uint16(dst), lqi), src)
		})
	// Weak devices — note this is the SNIFFER's vantage, not the network's.
	q(`SELECT addr,last_rssi FROM devices WHERE last_rssi < -85 AND count>=2 ORDER BY last_rssi ASC LIMIT 30`,
		func(r *sql.Rows) {
			var addr, rssi int
			r.Scan(&addr, &rssi)
			add("info", "Far from sniffer ⌖", fmt.Sprintf("RSSI %d dBm at the SNIFFER — this is the sniffer's distance to the device, not the device's link to its router. Check Net LQI (HA) or run an active test from a nearer radio", rssi), addr)
		})
	// Channel noise (latest ED per channel).
	q(`SELECT channel, ed_dbm FROM ed_samples WHERE rowid IN (SELECT MAX(rowid) FROM ed_samples GROUP BY channel)`,
		func(r *sql.Rows) {
			var ch, ed int
			r.Scan(&ch, &ed)
			if ed > -75 {
				add("warn", "Channel noise", fmt.Sprintf("channel %d energy %d dBm — busy; consider a quieter Zigbee channel (15/20/25)", ch, ed), -1)
			}
		})
	if len(out) == 0 {
		add("info", "All clear", "no link/route/signal issues detected yet — keep capturing", -1)
	}
	return out
}

// Messages returns recent packets to/from a device (by short address int).
func (d *DB) Messages(addr, limit int) []map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, _ := d.db.Query(`SELECT ts,channel,rssi,lqi,ftype,summary,decrypted
	                       FROM packets WHERE src=? OR dst=? ORDER BY id DESC LIMIT ?`,
		addr, addr, limit)
	var out []map[string]any
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var ts sql.NullFloat64
			var ch, rssi, lqi, dec sql.NullInt64
			var ftype, summary sql.NullString
			rows.Scan(&ts, &ch, &rssi, &lqi, &ftype, &summary, &dec)
			out = append(out, map[string]any{
				"ts": ts.Float64, "channel": ch.Int64, "rssi": rssi.Int64, "lqi": lqi.Int64,
				"type": ftype.String, "summary": summary.String, "decrypted": dec.Int64 == 1,
			})
		}
	}
	return out
}

// Packets returns the most recent captured packets (for export / frame log).
func (d *DB) Packets(limit int) []map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, _ := d.db.Query(`SELECT ts,radio,channel,rssi,lqi,src,dst,ftype,summary,decrypted
	                       FROM packets ORDER BY id DESC LIMIT ?`, limit)
	var out []map[string]any
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var ts sql.NullFloat64
			var radio, ch, rssi, lqi, dec sql.NullInt64
			var src, dst sql.NullInt64
			var ftype, summary sql.NullString
			rows.Scan(&ts, &radio, &ch, &rssi, &lqi, &src, &dst, &ftype, &summary, &dec)
			out = append(out, map[string]any{
				"ts": ts.Float64, "radio": radio.Int64, "channel": ch.Int64,
				"rssi": rssi.Int64, "lqi": lqi.Int64, "src": hexAddr(src), "dst": hexAddr(dst),
				"type": ftype.String, "summary": summary.String, "decrypted": dec.Int64 == 1,
			})
		}
	}
	return out
}

// Spectrum returns recent ED samples.
func (d *DB) Spectrum() []map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, _ := d.db.Query(`SELECT ts,channel,ed_dbm,sweep_id FROM ed_samples ORDER BY ts DESC LIMIT 2000`)
	var out []map[string]any
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var ts sql.NullFloat64
			var ch, ed, sw sql.NullInt64
			rows.Scan(&ts, &ch, &ed, &sw)
			out = append(out, map[string]any{
				"ts": ts.Float64, "channel": ch.Int64, "ed_dbm": ed.Int64, "sweep_id": sw.Int64,
			})
		}
	}
	return out
}

// --- active-probe results ---------------------------------------------------

// IngestProbe records the outcome of one active probe.
func (d *DB) IngestProbe(ts float64, target int, port string, radio int, acked bool, rssi, lqi int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	a := 0
	if acked {
		a = 1
	}
	d.db.Exec(`INSERT INTO probe_results(ts,target,port,radio,acked,rssi,lqi) VALUES(?,?,?,?,?,?,?)`,
		ts, target, port, radio, a, rssi, lqi)
}

// ProbeHistory returns recent probe results, optionally filtered by target (<0 = all).
func (d *DB) ProbeHistory(target, limit int) []map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	q := `SELECT ts,target,port,radio,acked,rssi,lqi FROM probe_results`
	args := []any{}
	if target >= 0 {
		q += ` WHERE target=?`
		args = append(args, target)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, _ := d.db.Query(q, args...)
	var out []map[string]any
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var ts sql.NullFloat64
			var tgt, radio, acked, rssi, lqi sql.NullInt64
			var port sql.NullString
			rows.Scan(&ts, &tgt, &port, &radio, &acked, &rssi, &lqi)
			out = append(out, map[string]any{
				"ts": ts.Float64, "target": fmt.Sprintf("0x%04x", uint16(tgt.Int64)),
				"port": port.String, "radio": radio.Int64, "acked": acked.Int64 == 1,
				"rssi": rssi.Int64, "lqi": lqi.Int64,
			})
		}
	}
	return out
}

// ProbeSummary returns per-target totals/acks for success-rate display.
func (d *DB) ProbeSummary() map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, _ := d.db.Query(`SELECT target, COUNT(*), SUM(acked), MAX(ts) FROM probe_results GROUP BY target`)
	out := map[string]any{}
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var tgt, total, acks sql.NullInt64
			var last sql.NullFloat64
			rows.Scan(&tgt, &total, &acks, &last)
			out[fmt.Sprintf("0x%04x", uint16(tgt.Int64))] = map[string]any{
				"total": total.Int64, "acks": acks.Int64, "last": last.Float64,
			}
		}
	}
	return out
}

// --- schedules --------------------------------------------------------------

// Schedule is a recurring active test.
type Schedule struct {
	ID        int     `json:"id"`
	Target    int     `json:"-"`
	TargetHex string  `json:"target"`
	Pan       int     `json:"pan"`
	Port      string  `json:"port"`
	IntervalS int     `json:"interval_s"`
	Enabled   bool    `json:"enabled"`
	LastRun   float64 `json:"last_run"`
	Runs      int     `json:"runs"`
	Acks      int     `json:"acks"`
}

func (d *DB) AddSchedule(target, pan int, port string, intervalS int) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, _ := d.db.Exec(`INSERT INTO schedules(target,pan,port,interval_s,enabled,created,last_run,runs,acks)
	                   VALUES(?,?,?,?,1,?,0,0,0)`, target, pan, port, intervalS, nowSec())
	id, _ := r.LastInsertId()
	return int(id)
}

func (d *DB) DeleteSchedule(id int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.db.Exec(`DELETE FROM schedules WHERE id=?`, id)
}

func (d *DB) SetScheduleEnabled(id int, enabled bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e := 0
	if enabled {
		e = 1
	}
	d.db.Exec(`UPDATE schedules SET enabled=? WHERE id=?`, e, id)
}

func (d *DB) MarkScheduleRun(id int, ts float64, acked bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	inc := 0
	if acked {
		inc = 1
	}
	d.db.Exec(`UPDATE schedules SET last_run=?, runs=runs+1, acks=acks+? WHERE id=?`, ts, inc, id)
}

func (d *DB) Schedules() []Schedule {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, _ := d.db.Query(`SELECT id,target,pan,port,interval_s,enabled,last_run,runs,acks FROM schedules ORDER BY id`)
	var out []Schedule
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var s Schedule
			var en int
			var port sql.NullString
			rows.Scan(&s.ID, &s.Target, &s.Pan, &port, &s.IntervalS, &en, &s.LastRun, &s.Runs, &s.Acks)
			s.Enabled = en == 1
			s.Port = port.String
			s.TargetHex = fmt.Sprintf("0x%04x", uint16(s.Target))
			out = append(out, s)
		}
	}
	return out
}

func nowSec() float64 { return float64(time.Now().UnixNano()) / 1e9 }
