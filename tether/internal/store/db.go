// Package store persists packets, devices, links, ED samples, and incidents in
// SQLite (pure-Go driver, no cgo). Port of host/zbsniff/store/db.py.
package store

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"zbsniff/internal/decode"
)

const broadcast = 0xFFFF

// haLogAddrRe pulls a short-address hex like "0x558f" out of an HA/zigpy log
// message, if one is present — zigpy conventionally prefixes per-device log
// lines with it (e.g. "[0x558f:1:0x0006] ..."), but plenty of radio/
// transport-layer entries have no device address at all.
var haLogAddrRe = regexp.MustCompile(`0x[0-9a-fA-F]{4}`)

// linkSig is the last-observed signal quality on one link, as heard at the
// sniffer (subject to the same "not the device's real link" caveat as other
// sniffer-vantage signals — see the "Far from sniffer" diagnostic).
type linkSig struct{ lqi, rssi int }

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
CREATE INDEX IF NOT EXISTS idx_ed_ts ON ed_samples(ts);
CREATE INDEX IF NOT EXISTS idx_ed_channel ON ed_samples(channel);
CREATE INDEX IF NOT EXISTS idx_packets_ts ON packets(ts);
CREATE TABLE IF NOT EXISTS incidents(
 id INTEGER PRIMARY KEY AUTOINCREMENT, ts REAL, addr TEXT, reason TEXT,
 rssi INT, lqi INT, channel INT, ed INT, silent_s INT, raw TEXT);
CREATE INDEX IF NOT EXISTS idx_incidents_addr ON incidents(addr);
CREATE INDEX IF NOT EXISTS idx_packets_dst ON packets(dst);
CREATE TABLE IF NOT EXISTS route_failures(addr INTEGER PRIMARY KEY, count INT, last_reason TEXT, last_seen REAL);
-- route_failures.count above is a lifetime total across every reason mixed
-- together (many-to-one, non-tree-link, address-conflict, ...) — it can only
-- say "the most recent one was X", not "X happened N times". This tracks a
-- real per-reason lifetime count, e.g. so the Address Conflict diagnostic
-- isn't overstated by folding in unrelated route-failure reasons.
CREATE TABLE IF NOT EXISTS route_failure_reasons(
 addr INT, reason TEXT, count INT, last_seen REAL, PRIMARY KEY(addr,reason));
-- Timestamped route-failure events (route_failures above only keeps a
-- lifetime total per device). This is what root-cause clustering in
-- Diagnostics() uses to tell one device's real problem apart from a shared
-- cause (a bad router, interference, a mesh-wide hiccup) hitting many
-- devices at once. reporter is the NWK source of the Network-Status frame —
-- i.e. who is reporting the failure, not who it's about.
CREATE TABLE IF NOT EXISTS route_failure_log(
 id INTEGER PRIMARY KEY AUTOINCREMENT, ts REAL, target INT, reporter INT, reason TEXT);
CREATE INDEX IF NOT EXISTS idx_rfl_ts ON route_failure_log(ts);
CREATE INDEX IF NOT EXISTS idx_rfl_target ON route_failure_log(target);
-- Home Assistant's own system log (Settings > Logs), pulled over the existing
-- HA websocket link and filtered to ZHA/zigpy-sourced entries. This is the
-- layer a passive Zigbee sniffer can't see: a command that never made it
-- onto the air, a timeout, an exception inside the integration itself — all
-- invisible on the RF side. key mirrors HA's own dedup (logger+first
-- occurrence) so a repeated log line updates in place instead of piling up.
CREATE TABLE IF NOT EXISTS ha_log(
 key TEXT PRIMARY KEY, ts REAL, first_seen REAL, level TEXT, logger TEXT,
 message TEXT, exception TEXT, count INT);
CREATE INDEX IF NOT EXISTS idx_ha_log_ts ON ha_log(ts);
CREATE TABLE IF NOT EXISTS pans(pan INTEGER PRIMARY KEY, count INT, last_seen REAL, channel INT, label TEXT);
-- Per-minute airtime rollup: frames + RSSI histogram per (PAN, channel).
-- Aggregated at ingest so the airtime view never scans the packets table
-- (see the edCap note above for why a heavy query here freezes the app).
-- pan = -1 collects frames with no PAN field at all (MAC ACKs etc.).
-- h0..h7 are RSSI bins: <=-101, then 10 dB steps up to >=-40.
CREATE TABLE IF NOT EXISTS pan_minutes(
 bucket INT, pan INT, channel INT, frames INT, rssi_sum INT,
 h0 INT DEFAULT 0, h1 INT DEFAULT 0, h2 INT DEFAULT 0, h3 INT DEFAULT 0,
 h4 INT DEFAULT 0, h5 INT DEFAULT 0, h6 INT DEFAULT 0, h7 INT DEFAULT 0,
 PRIMARY KEY(bucket,pan,channel));
CREATE INDEX IF NOT EXISTS idx_pan_minutes_bucket ON pan_minutes(bucket);
CREATE TABLE IF NOT EXISTS probe_results(
 id INTEGER PRIMARY KEY AUTOINCREMENT, ts REAL, target INT, port TEXT, radio INT,
 acked INT, rssi INT, lqi INT);
CREATE INDEX IF NOT EXISTS idx_probe_target ON probe_results(target);
CREATE TABLE IF NOT EXISTS schedules(
 id INTEGER PRIMARY KEY AUTOINCREMENT, target INT, pan INT, port TEXT,
 interval_s INT, enabled INT, created REAL, last_run REAL, runs INT, acks INT);
`

// edCap bounds the ed_samples table. A continuous ED sweep writes thousands of
// rows/minute; left unbounded the table reaches millions of rows and the
// Spectrum/diagnostics queries (which scan it) take seconds — and because every
// DB call is serialized under d.mu, that slow query starves frame ingest and
// freezes the whole app. We keep only the most recent edCap rows.
const edCap = 60000

// DB wraps the SQLite connection.
type DB struct {
	mu       sync.Mutex
	db       *sql.DB
	edWrites  int // counter for periodic ed_samples pruning
	pktWrites int // counter for periodic packets pruning
	rflWrites int // counter for periodic route_failure_log pruning
	pmWrites  int // counter for periodic pan_minutes pruning
}

// Open opens (or creates) the database at path (":memory:" for in-memory).
func Open(path string) (*DB, error) {
	d, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1) // serialize; simplest correct option for SQLite
	// WAL + a busy timeout keep writes from blocking as hard under read load.
	for _, p := range []string{
		"PRAGMA journal_mode=WAL", "PRAGMA synchronous=NORMAL", "PRAGMA busy_timeout=5000",
	} {
		d.Exec(p)
	}
	if _, err := d.Exec(schema); err != nil {
		return nil, err
	}
	// Migrate older DBs (ignore "duplicate column" errors).
	d.Exec(`ALTER TABLE pans ADD COLUMN label TEXT`)
	d.Exec(`ALTER TABLE devices ADD COLUMN pan INTEGER`) // which network the device is on
	d.Exec(`ALTER TABLE packets ADD COLUMN raw TEXT`)    // raw MPDU hex, for the frame inspector
	// 'mac' (immediate physical radio hop) or 'nwk' (logical end-to-end device
	// pair — same across every hop of a multi-hop route, so it does NOT reveal
	// intermediate routers). NULL for pre-migration rows. See upsertLink().
	d.Exec(`ALTER TABLE links ADD COLUMN layer TEXT`)
	// One-time trim: an existing DB may already hold millions of ed_samples from
	// before the cap existed. Bring it back under edCap so queries are fast now.
	d.Exec(`DELETE FROM ed_samples WHERE rowid <= (SELECT MAX(rowid) - ? FROM ed_samples)`, edCap)
	return &DB{db: d}, nil
}

// Reset purges every captured-data table back to empty (packets, devices,
// links, ED samples, incidents, route failures, PANs, probe history,
// schedules) — used by the dashboard's "purge data"/"factory reset" controls.
// Settings (zbsniff.yaml) are untouched; see main.go's factory-reset handler
// for the variant that also resets those. VACUUM reclaims the freed disk
// space, which SQLite doesn't do automatically after a DELETE.
func (d *DB) Reset() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tables := []string{"packets", "devices", "links", "ed_samples", "incidents",
		"route_failures", "route_failure_log", "route_failure_reasons", "ha_log", "pans", "probe_results", "schedules"}
	for _, t := range tables {
		if _, err := d.db.Exec("DELETE FROM " + t); err != nil {
			return fmt.Errorf("purge %s: %w", t, err)
		}
	}
	d.db.Exec("VACUUM")
	return nil
}

func hexAddr(a sql.NullInt64) string {
	if !a.Valid {
		return ""
	}
	return fmt.Sprintf("0x%04x", uint16(a.Int64))
}

// IngestFrame stores a decoded frame and updates device/link aggregates.
func (d *DB) IngestFrame(ts float64, radio, channel int, rssi, lqi int, dec *decode.Decoded, raw []byte) {
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
	d.db.Exec(`INSERT INTO packets(ts,radio,channel,rssi,lqi,src,dst,ftype,summary,decrypted,raw)
	           VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		ts, radio, channel, rssi, lqi, srcN, dstN, dec.MAC.TypeName, dec.Summary(), decrypted,
		hex.EncodeToString(raw))
	// Bound the packets table (raw bytes make rows bigger). Keep the newest ~300k.
	if d.pktWrites++; d.pktWrites%4096 == 0 {
		d.db.Exec(`DELETE FROM packets WHERE id <= (SELECT MAX(id) - 300000 FROM packets)`)
	}

	// Track PAN ids seen on-air (to flag foreign Zigbee networks). Ignore the
	// broadcast PAN 0xFFFF and "not present". Beacons (an active scan's replies)
	// carry the responding network's PAN as the *source* PAN, not the dest, so
	// record whichever is present.
	seenPan := dec.MAC.DstPan
	if seenPan < 0 || seenPan == 0xFFFF {
		seenPan = dec.MAC.SrcPan
	}
	if seenPan >= 0 && seenPan != 0xFFFF {
		d.db.Exec(`INSERT INTO pans(pan,count,last_seen,channel) VALUES(?,1,?,?)
		           ON CONFLICT(pan) DO UPDATE SET count=count+1,last_seen=?,channel=?`,
			seenPan, ts, channel, ts, channel)
	}

	panInt := -1
	if seenPan >= 0 && seenPan != 0xFFFF {
		panInt = int(seenPan)
	}

	// Per-minute airtime rollup (frames + RSSI histogram per PAN/channel).
	// panInt -1 keeps ACKs and other PAN-less frames counted as airtime.
	bucket := int(ts) / 60
	bin := (rssi + 110) / 10
	if bin < 0 {
		bin = 0
	} else if bin > 7 {
		bin = 7
	}
	col := histCols[bin]
	d.db.Exec(`INSERT INTO pan_minutes(bucket,pan,channel,frames,rssi_sum,`+col+`)
	           VALUES(?,?,?,1,?,1)
	           ON CONFLICT(bucket,pan,channel) DO UPDATE SET
	           frames=frames+1,rssi_sum=rssi_sum+?,`+col+`=`+col+`+1`,
		bucket, panInt, channel, rssi, rssi)
	if d.pmWrites++; d.pmWrites%4096 == 0 {
		d.db.Exec(`DELETE FROM pan_minutes WHERE bucket < ?`, bucket-2880) // keep 48h
	}
	// MAC-layer addresses are the physical hop (often router<->coordinator).
	if src >= 0 && src != broadcast {
		d.upsertDevice(int(src), ts, rssi, lqi, channel, panInt)
	}
	if src >= 0 && dst >= 0 && dst != broadcast {
		d.upsertLink(int(src), int(dst), lqi, rssi, ts, "mac")
	}

	// NWK-layer addresses are the actual source/destination DEVICES (the NWK
	// header is unencrypted, so this works even without the network key). This is
	// what reveals child devices that only ever talk through a router. Tagged
	// "nwk" (not "mac") because this pair is the same across every hop of a
	// multi-hop route — it does NOT reveal the intermediate router(s) the way
	// the MAC-layer hop above does, and the routing tree must not treat it as
	// one (see Routing()/latestEdgePerSrc in the api package).
	if dec.NWK != nil {
		ns, nd := dec.NWK.Src, dec.NWK.Dst
		if ns >= 0 && ns < 0xFFF8 {
			d.upsertDevice(ns, ts, rssi, lqi, channel, panInt)
		}
		if ns >= 0 && ns < 0xFFF8 && nd >= 0 && nd < 0xFFF8 && ns != nd {
			d.upsertLink(ns, nd, lqi, rssi, ts, "nwk")
		}
	}
}

func (d *DB) upsertLink(src, dst, lqi, rssi int, ts float64, layer string) {
	d.db.Exec(`INSERT INTO links(src,dst,count,last_lqi,last_rssi,last_seen,layer) VALUES(?,?,1,?,?,?,?)
	           ON CONFLICT(src,dst) DO UPDATE SET count=count+1,last_lqi=?,last_rssi=?,last_seen=?,layer=?`,
		src, dst, lqi, rssi, ts, layer, lqi, rssi, ts, layer)
}

func (d *DB) upsertDevice(addr int, ts float64, rssi, lqi, channel, pan int) {
	var cnt int
	err := d.db.QueryRow(`SELECT count FROM devices WHERE addr=?`, addr).Scan(&cnt)
	role := any(nil)
	if addr == 0 {
		role = "coordinator"
	}
	var panN any
	if pan >= 0 {
		panN = pan
	}
	if err == sql.ErrNoRows {
		d.db.Exec(`INSERT INTO devices(addr,role,first_seen,last_seen,count,last_rssi,last_lqi,last_channel,pan)
		           VALUES(?,?,?,?,1,?,?,?,?)`, addr, role, ts, ts, rssi, lqi, channel, panN)
	} else {
		// First PAN seen sticks (COALESCE(pan,?)) — stable, and avoids flip-flop
		// from short-address collisions across networks (every net has a 0x0000).
		d.db.Exec(`UPDATE devices SET last_seen=?,count=count+1,last_rssi=?,last_lqi=?,last_channel=?,pan=COALESCE(pan,?) WHERE addr=?`,
			ts, rssi, lqi, channel, panN, addr)
	}
}

// SetDeviceExt persists a device's extended (IEEE) address so its name/vendor
// survive restarts (the short<->IEEE map is otherwise learned on-air each run).
func (d *DB) SetDeviceExt(addr int, ext uint64) {
	if ext == 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.db.Exec(`UPDATE devices SET ext=? WHERE addr=?`, int64(ext), addr)
}

// SetDeviceName persists a resolved device name (e.g. from a paired Hue bridge)
// so it survives restarts. Only fills an empty name — a live HA/ZHA name is
// resolved at read time and shouldn't be shadowed by a stored one.
func (d *DB) SetDeviceName(addr int, name string) {
	if name == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.db.Exec(`UPDATE devices SET name=? WHERE addr=? AND (name IS NULL OR name='')`, name, addr)
}

// DeviceExts returns every persisted short->IEEE mapping, to reseed the name
// registry on startup.
func (d *DB) DeviceExts() map[int]uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[int]uint64{}
	rows, _ := d.db.Query(`SELECT addr,ext FROM devices WHERE ext IS NOT NULL AND ext!=0`)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var addr int
			var ext int64
			rows.Scan(&addr, &ext)
			out[addr] = uint64(ext)
		}
	}
	return out
}

// IngestED stores an energy-detect sample, pruning old rows periodically so the
// table stays bounded (see edCap).
func (d *DB) IngestED(ts float64, channel, edDBm, sweepID, radio int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.db.Exec(`INSERT INTO ed_samples(ts,channel,ed_dbm,sweep_id,radio) VALUES(?,?,?,?,?)`,
		ts, channel, edDBm, sweepID, radio)
	// Amortize pruning: every 1024 inserts, drop everything beyond the newest
	// edCap rows. rowid is monotonic, so this is an indexed range delete.
	if d.edWrites++; d.edWrites%1024 == 0 {
		d.db.Exec(`DELETE FROM ed_samples WHERE rowid <= (SELECT MAX(rowid) - ? FROM ed_samples)`, edCap)
	}
}

// IngestIncident stores an incident from the device JSON.
func (d *DB) IngestIncident(data map[string]any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.insertIncidentLocked(data)
}

// insertIncidentLocked writes one incident row. Caller must hold d.mu.
func (d *DB) insertIncidentLocked(data map[string]any) {
	raw, _ := json.Marshal(data)
	get := func(k string) any { return data[k] }
	d.db.Exec(`INSERT INTO incidents(ts,addr,reason,rssi,lqi,channel,ed,silent_s,raw)
	           VALUES(?,?,?,?,?,?,?,?,?)`,
		get("ts"), get("addr"), get("reason"), get("rssi"), get("lqi"),
		get("ch"), get("ed"), get("silent_s"), string(raw))
}

// incidentMinFrames: only devices we've heard at least this many times are
// candidates — avoids flagging a device seen once in passing.
const incidentMinFrames = 5

// DetectIncidents scans known devices for silence and recovery against
// thresholdS (seconds) and logs any NEW incidents, stamping each with the
// channel's energy at detection time (the "was it interference?" evidence).
// Down-state is derived from the incidents table itself (latest silence vs
// recovered per device), so it survives host restarts and never double-fires.
// Returns the incidents it just logged so the caller can broadcast them live.
func (d *DB) DetectIncidents(thresholdS int, now float64) []map[string]any {
	if thresholdS <= 0 {
		thresholdS = 120
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	// If the sniffer itself has heard NOTHING for a full threshold window, this is
	// a capture outage (link/radio dropped), not devices failing — every device
	// would trip at once (including the coordinator). Suppress the false flood;
	// real dropouts are detected while other devices are still being heard.
	var maxSeen sql.NullFloat64
	d.db.QueryRow(`SELECT MAX(last_seen) FROM devices`).Scan(&maxSeen)
	if maxSeen.Valid && now-maxSeen.Float64 >= float64(thresholdS) {
		return nil
	}

	// Latest energy reading per channel (approximate "energy now").
	edByCh := map[int]int{}
	if rows, err := d.db.Query(`SELECT channel, ed_dbm FROM ed_samples WHERE rowid IN (SELECT MAX(rowid) FROM ed_samples GROUP BY channel)`); err == nil {
		for rows.Next() {
			var ch, ed int
			rows.Scan(&ch, &ed)
			edByCh[ch] = ed
		}
		rows.Close()
	}

	// Current down-state: the most recent silence/recovered incident per device.
	down := map[string]bool{}
	if rows, err := d.db.Query(`SELECT addr, reason FROM incidents
	    WHERE id IN (SELECT MAX(id) FROM incidents WHERE reason IN ('silence','recovered') GROUP BY addr)`); err == nil {
		for rows.Next() {
			var a, r string
			rows.Scan(&a, &r)
			down[a] = r == "silence"
		}
		rows.Close()
	}

	// Snapshot candidate devices, then evaluate (can't insert while iterating the
	// same connection's rows).
	type cand struct {
		addr, rssi, lqi, ch int
		last                float64
	}
	var cands []cand
	if rows, err := d.db.Query(`SELECT addr,last_seen,last_rssi,last_lqi,last_channel,count
	    FROM devices WHERE count >= ?`, incidentMinFrames); err == nil {
		for rows.Next() {
			var c cand
			var cnt int
			rows.Scan(&c.addr, &c.last, &c.rssi, &c.lqi, &c.ch, &cnt)
			cands = append(cands, c)
		}
		rows.Close()
	}

	var logged []map[string]any
	for _, c := range cands {
		if c.last <= 0 {
			continue
		}
		silent := int(now - c.last)
		addrHex := fmt.Sprintf("0x%04x", uint16(c.addr))
		isDown := down[addrHex]
		switch {
		case silent >= thresholdS && !isDown:
			inc := map[string]any{
				"ts": now, "addr": addrHex, "reason": "silence",
				"rssi": c.rssi, "lqi": c.lqi, "ch": c.ch,
				"ed": edByCh[c.ch], "silent_s": silent,
			}
			d.insertIncidentLocked(inc)
			logged = append(logged, inc)
		case silent < thresholdS && isDown:
			inc := map[string]any{
				"ts": now, "addr": addrHex, "reason": "recovered",
				"rssi": c.rssi, "lqi": c.lqi, "ch": c.ch, "silent_s": silent,
			}
			d.insertIncidentLocked(inc)
			logged = append(logged, inc)
		}
	}
	return logged
}

// Devices returns the device registry as JSON-ready maps.
func (d *DB) Devices() []map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.db.Query(`SELECT addr,role,name,last_seen,count,last_rssi,last_lqi,last_channel,pan
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
		var count, rssi, lqi, ch, pan sql.NullInt64
		rows.Scan(&addr, &role, &name, &lastSeen, &count, &rssi, &lqi, &ch, &pan)
		m := map[string]any{
			"addr": fmt.Sprintf("0x%04x", uint16(addr)), "role": role.String, "name": name.String,
			"count": count.Int64, "last_rssi": rssi.Int64, "last_lqi": lqi.Int64,
			"last_channel": ch.Int64,
		}
		if pan.Valid {
			m["pan"] = fmt.Sprintf("0x%04x", uint16(pan.Int64))
		}
		out = append(out, m)
	}
	return out
}

// Routing returns nodes + edges for the routing view.
func (d *DB) Routing() map[string]any {
	devices := d.Devices()
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, _ := d.db.Query(`SELECT src,dst,count,last_lqi,last_rssi,last_seen,layer FROM links ORDER BY count DESC`)
	var edges []map[string]any
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var src, dst sql.NullInt64
			var count, lqi, rssi sql.NullInt64
			var lastSeen sql.NullFloat64
			var layer sql.NullString
			rows.Scan(&src, &dst, &count, &lqi, &rssi, &lastSeen, &layer)
			edges = append(edges, map[string]any{
				"src": hexAddr(src), "dst": hexAddr(dst),
				"lqi": lqi.Int64, "rssi": rssi.Int64, "count": count.Int64,
				"last_seen": lastSeen.Float64, "layer": layer.String,
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

// rflCap bounds route_failure_log the same way edCap bounds ed_samples — keep
// only the most recent rflCap events so the time-clustering query in
// routeFailureClusterFindings stays fast over weeks of capture.
const rflCap = 20000

// IngestRouteFailure records an NWK route/link failure targeting a device:
// both a lifetime per-device total (route_failures — unchanged, immediately
// useful even with no history) and a timestamped event (route_failure_log —
// what root-cause clustering uses to tell one device's real problem apart
// from a shared cause hitting many devices at once). reporter is the NWK
// source of the Network-Status frame, i.e. who is reporting the failure.
func (d *DB) IngestRouteFailure(target, reporter int, reason string, ts float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.db.Exec(`INSERT INTO route_failures(addr,count,last_reason,last_seen) VALUES(?,1,?,?)
	           ON CONFLICT(addr) DO UPDATE SET count=count+1,last_reason=?,last_seen=?`,
		target, reason, ts, reason, ts)
	d.db.Exec(`INSERT INTO route_failure_reasons(addr,reason,count,last_seen) VALUES(?,?,1,?)
	           ON CONFLICT(addr,reason) DO UPDATE SET count=count+1,last_seen=?`,
		target, reason, ts, ts)
	d.db.Exec(`INSERT INTO route_failure_log(ts,target,reporter,reason) VALUES(?,?,?,?)`,
		ts, target, reporter, reason)
	if d.rflWrites++; d.rflWrites%512 == 0 {
		d.db.Exec(`DELETE FROM route_failure_log WHERE rowid <= (SELECT MAX(rowid) - ? FROM route_failure_log)`, rflCap)
	}
}

// routeFailureClusterGapS bounds how close in time two route-failure events
// must be to count as the same underlying incident rather than an unrelated
// coincidence — single-linkage time clustering: walk events in time order and
// start a new cluster whenever the gap since the last one exceeds this.
const routeFailureClusterGapS = 300 // 5 minutes

type rfEvent struct {
	ts               float64
	target, reporter int
	reason           string
}

// routeFailureClusterFindings turns the raw route_failure_log into a handful
// of root-cause findings instead of one alert per device. It clusters events
// by time proximity into incidents, then classifies each incident by what the
// events in it have in common:
//   - one reporting router, many different targets → the router itself (its
//     uplink, load, or firmware) is the likely shared cause, not N broken
//     devices
//   - one target, regardless of reporter → that device genuinely is hard to
//     reach
//   - neither → a mesh-wide event (congestion, interference, a coordinator
//     hiccup), correlated against the channel's current energy for an
//     interference hypothesis
//
// Address-conflict events are excluded — they're handled separately in
// Diagnostics() from the lifetime totals, since that signal doesn't need
// time-clustering and (unlike this) has no cold-start problem after an
// upgrade. Caller must already hold d.mu (this issues its own queries against
// the shared *sql.DB, same pattern as the rest of Diagnostics()).
func routeFailureClusterFindings(db *sql.DB, edByCh map[int]int) []map[string]any {
	rows, err := db.Query(`SELECT ts,target,reporter,reason FROM route_failure_log
	                       WHERE reason != 'Address conflict' ORDER BY ts ASC`)
	if err != nil {
		return nil
	}
	var events []rfEvent
	for rows.Next() {
		var e rfEvent
		var reporter sql.NullInt64
		rows.Scan(&e.ts, &e.target, &reporter, &e.reason)
		e.reporter = int(reporter.Int64)
		events = append(events, e)
	}
	rows.Close()
	if len(events) == 0 {
		return nil
	}

	var clusters [][]rfEvent
	cur := []rfEvent{events[0]}
	for _, e := range events[1:] {
		if e.ts-cur[len(cur)-1].ts > routeFailureClusterGapS {
			clusters = append(clusters, cur)
			cur = nil
		}
		cur = append(cur, e)
	}
	clusters = append(clusters, cur)

	noisiestCh := -200
	for _, ed := range edByCh {
		if ed > noisiestCh {
			noisiestCh = ed
		}
	}

	var out []map[string]any
	add := func(sev, cat, msg string, addr int, ts float64) {
		m := map[string]any{"severity": sev, "category": cat, "message": msg}
		if addr >= 0 {
			m["addr"] = fmt.Sprintf("0x%04x", uint16(addr))
		}
		if ts > 0 {
			m["ts"] = ts
		}
		out = append(out, m)
	}

	// Biggest incidents first; a long tail of 2-event blips isn't worth
	// showing, and there's a cap so a bad week doesn't flood the panel.
	sort.Slice(clusters, func(i, j int) bool { return len(clusters[i]) > len(clusters[j]) })
	shown := 0
	for _, cl := range clusters {
		if len(cl) < 2 {
			continue
		}
		if shown >= 10 {
			break
		}
		targets, reporters, reasons := map[int]int{}, map[int]int{}, map[string]int{}
		for _, e := range cl {
			targets[e.target]++
			if e.reporter != 0 {
				reporters[e.reporter]++
			}
			reasons[e.reason]++
		}
		dur := cl[len(cl)-1].ts - cl[0].ts
		reason := modeKey(reasons)
		sev := "notice"
		switch {
		case len(cl) >= 20:
			sev = "critical"
		case len(cl) >= 8:
			sev = "error"
		case len(cl) >= 3:
			sev = "warning"
		}
		switch {
		case len(reporters) == 1 && len(targets) >= 3:
			rep := onlyIntKey(reporters)
			add(sev, "Router health", fmt.Sprintf(
				"router 0x%04x reported %d route failures (\"%s\") for %d different devices over %s — the shared cause is likely this router itself (its uplink, load, or firmware), not %d unrelated device faults",
				uint16(rep), len(cl), reason, len(targets), humanDur(dur), len(targets)), rep, cl[len(cl)-1].ts)
		case len(targets) == 1:
			tgt := onlyIntKey(targets)
			add(sev, "Route failure", fmt.Sprintf(
				"%d route failures (\"%s\") over %s — this device specifically is hard to reach; re-pair it or add a router nearer to it",
				len(cl), reason, humanDur(dur)), tgt, cl[len(cl)-1].ts)
		default:
			note := ""
			if noisiestCh > -75 {
				note = fmt.Sprintf(" — channel energy is currently elevated (%ddBm), consistent with RF interference as a shared cause", noisiestCh)
			}
			add(sev, "Mesh event", fmt.Sprintf(
				"%d route failures across %d devices and %d routers within %s — looks like a shared event (congestion, interference, or a coordinator hiccup), not %d independent device faults%s",
				len(cl), len(targets), len(reporters), humanDur(dur), len(targets), note), -1, cl[len(cl)-1].ts)
		}
		shown++
	}
	return out
}

func modeKey(m map[string]int) string {
	best, bestN := "", -1
	for k, n := range m {
		if n > bestN {
			best, bestN = k, n
		}
	}
	return best
}
func onlyIntKey(m map[int]int) int {
	best, bestN := 0, -1
	for k, n := range m {
		if n > bestN {
			best, bestN = k, n
		}
	}
	return best
}
func humanDur(s float64) string {
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", int(s))
	case s < 3600:
		return fmt.Sprintf("%dm", int(s/60))
	default:
		return fmt.Sprintf("%.1fh", s/3600)
	}
}

// IngestHALog upserts one ZHA/zigpy-sourced Home Assistant system-log entry
// (already filtered by the caller — see names.HAClient.Fetch). key mirrors
// HA's own dedup (logger name + first occurrence) so a repeated log line
// updates count/timestamp in place instead of accumulating duplicate rows.
func (d *DB) IngestHALog(key string, ts, firstSeen float64, level, logger, message, exception string, count int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.db.Exec(`INSERT INTO ha_log(key,ts,first_seen,level,logger,message,exception,count) VALUES(?,?,?,?,?,?,?,?)
	           ON CONFLICT(key) DO UPDATE SET ts=excluded.ts,count=excluded.count,message=excluded.message`,
		key, ts, firstSeen, level, logger, message, exception, count)
}

// HALogsFor returns HA system-log entries whose message mentions any of
// needles (typically a device's short-address hex like "0x558f", its IEEE,
// and/or its friendly name — zigpy log lines conventionally prefix with the
// short address, e.g. "[0x558f:1:0x0006] ..."). This is the layer a passive
// sniffer can't see: a command that never made it onto the air, a timeout,
// an exception inside the integration — so it's checked alongside RF-derived
// findings in DeviceAnalysis, not as a replacement for them.
func (d *DB) HALogsFor(needles []string, limit int) []map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	var conds []string
	var args []any
	for _, n := range needles {
		if n == "" {
			continue
		}
		conds = append(conds, "message LIKE ?")
		args = append(args, "%"+n+"%")
	}
	if len(conds) == 0 {
		return nil
	}
	q := fmt.Sprintf(`SELECT ts,level,logger,message,exception,count FROM ha_log WHERE (%s) ORDER BY ts DESC LIMIT ?`,
		strings.Join(conds, " OR "))
	args = append(args, limit)
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var ts float64
		var level, logger, message, exception string
		var count int
		rows.Scan(&ts, &level, &logger, &message, &exception, &count)
		out = append(out, map[string]any{
			"ts": ts, "level": level, "logger": logger, "message": message,
			"exception": exception, "count": count,
		})
	}
	return out
}

// DeviceAnalysis aggregates every record the store holds about one device —
// its full silence/recovery incident history, route failures, active-probe
// results, and RSSI/LQI trend at the sniffer — plus a set of rule-based
// findings that try to explain *why* it is going unresponsive. It's the
// single-device counterpart to Diagnostics(), which scans the whole mesh.
func (d *DB) DeviceAnalysis(addr int) map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	addrHex := fmt.Sprintf("0x%04x", uint16(addr))

	dev := map[string]any{}
	{
		var firstSeen, lastSeen sql.NullFloat64
		var count, rssi, lqi, ch sql.NullInt64
		var role, name sql.NullString
		row := d.db.QueryRow(`SELECT first_seen,last_seen,count,last_rssi,last_lqi,last_channel,role,name
		                      FROM devices WHERE addr=?`, addr)
		if err := row.Scan(&firstSeen, &lastSeen, &count, &rssi, &lqi, &ch, &role, &name); err == nil {
			dev = map[string]any{
				"first_seen": firstSeen.Float64, "last_seen": lastSeen.Float64, "count": count.Int64,
				"last_rssi": rssi.Int64, "last_lqi": lqi.Int64, "last_channel": ch.Int64,
				"role": role.String, "name": name.String,
			}
		}
	}

	// Full incident history for this device (not just the latest N, like
	// Incidents() returns for the mesh-wide feed) — oldest first, so trend
	// analysis below can walk it in order.
	var incidents []map[string]any
	if rows, err := d.db.Query(`SELECT ts,reason,rssi,lqi,channel,ed,silent_s
	                            FROM incidents WHERE addr=? ORDER BY ts ASC`, addrHex); err == nil {
		for rows.Next() {
			var ts sql.NullFloat64
			var reason sql.NullString
			var rssi, lqi, ch, ed, silent sql.NullInt64
			rows.Scan(&ts, &reason, &rssi, &lqi, &ch, &ed, &silent)
			incidents = append(incidents, map[string]any{
				"ts": ts.Float64, "reason": reason.String, "rssi": rssi.Int64, "lqi": lqi.Int64,
				"channel": ch.Int64, "ed": ed.Int64, "silent_s": silent.Int64,
			})
		}
		rows.Close()
	}

	var rf map[string]any
	{
		var count sql.NullInt64
		var reason sql.NullString
		var lastSeen sql.NullFloat64
		if err := d.db.QueryRow(`SELECT count,last_reason,last_seen FROM route_failures WHERE addr=?`, addr).
			Scan(&count, &reason, &lastSeen); err == nil {
			rf = map[string]any{"count": count.Int64, "last_reason": reason.String, "last_seen": lastSeen.Float64}
		}
	}

	// RSSI/LQI trend at the sniffer, oldest-first, capped so a chatty device
	// doesn't blow up the response or the query.
	var trend []map[string]any
	if rows, err := d.db.Query(`SELECT ts,rssi,lqi FROM packets WHERE src=? OR dst=?
	                            ORDER BY id DESC LIMIT 300`, addr, addr); err == nil {
		for rows.Next() {
			var ts sql.NullFloat64
			var rssi, lqi sql.NullInt64
			rows.Scan(&ts, &rssi, &lqi)
			trend = append(trend, map[string]any{"ts": ts.Float64, "rssi": rssi.Int64, "lqi": lqi.Int64})
		}
		rows.Close()
		for i, j := 0, len(trend)-1; i < j; i, j = i+1, j-1 { // → oldest-first
			trend[i], trend[j] = trend[j], trend[i]
		}
	}

	var probeTotal, probeAcks sql.NullInt64
	d.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(acked),0) FROM probe_results WHERE target=?`, addr).
		Scan(&probeTotal, &probeAcks)

	// Probe timestamps, for correlating against rejoin incidents below — did
	// an active probe precede a recovery, suggesting probing this device
	// nudges it back online rather than it recovering on its own?
	var probeTimes []float64
	if rows, err := d.db.Query(`SELECT ts FROM probe_results WHERE target=? ORDER BY ts ASC`, addr); err == nil {
		for rows.Next() {
			var ts float64
			rows.Scan(&ts)
			probeTimes = append(probeTimes, ts)
		}
		rows.Close()
	}

	// Every distinct link partner we've ever observed for this device — used
	// to spot a structural single-point-of-failure: a device whose only known
	// path is one direct hop to the coordinator, with no nearby router to
	// fall back on. That alone explains "sometimes just stops responding
	// (NWK_NO_ROUTE), but the RF trace looks otherwise fine": when the one
	// link degrades even briefly, there's nowhere else for a message to go.
	neighbors := map[int]linkSig{}
	if rows, err := d.db.Query(`SELECT src,dst,last_lqi,last_rssi FROM links WHERE src=? OR dst=?`, addr, addr); err == nil {
		for rows.Next() {
			var src, dst, lqi, rssi int
			rows.Scan(&src, &dst, &lqi, &rssi)
			other := dst
			if src != addr {
				other = src
			}
			neighbors[other] = linkSig{lqi, rssi}
		}
		rows.Close()
	}

	return map[string]any{
		"addr": addrHex, "device": dev, "incidents": incidents, "route_failures": rf,
		"rssi_trend": trend, "probes": map[string]any{"total": probeTotal.Int64, "acks": probeAcks.Int64},
		"findings": deviceAnalysisFindings(incidents, rf, trend, probeTotal.Int64, probeAcks.Int64, probeTimes, neighbors),
	}
}

// deviceAnalysisFindings turns one device's records into plain-English
// hypotheses for why it might be going unresponsive. Same {severity,
// category, message} shape and severity scale as Diagnostics().
func deviceAnalysisFindings(incidents []map[string]any, rf map[string]any, trend []map[string]any,
	probeTotal, probeAcks int64, probeTimes []float64, neighbors map[int]linkSig) []map[string]any {
	var out []map[string]any
	add := func(sev, cat, msg string) { out = append(out, map[string]any{"severity": sev, "category": cat, "message": msg}) }

	// Topology risk: if the only link we've ever observed for this device is
	// a direct hop straight to the coordinator, it has no fallback path — a
	// structural single point of failure, independent of current signal
	// quality. When that one link degrades even briefly (interference, a busy
	// moment on the coordinator's radio), there's nowhere else to route,
	// which fits a device that intermittently refuses commands (e.g.
	// NWK_NO_ROUTE) or looks unresponsive despite an otherwise clean RF trace.
	if len(neighbors) == 1 {
		if sig, ok := neighbors[0]; ok {
			add("warning", "Topology risk", fmt.Sprintf(
				"the only path ever observed for this device is a direct link to the coordinator (LQI %d, %d dBm as heard at the sniffer) — no nearby router to fall back on if that link degrades. A mains-powered router/repeater placed between the coordinator and this device would give it a real alternate path",
				sig.lqi, sig.rssi))
		}
	}

	// Rejoins: a real Association/Orphan/Coordinator-Realignment/Device-
	// announce — much stronger evidence than inferred silence/recovery that
	// this device actually lost and re-established its network connection.
	// The key question: does probing it correlate with recovery? If an
	// active probe reliably precedes a rejoin, that's a strong, actionable
	// clue (the device is orphaning, not failing outright — a nudge brings
	// it back) rather than a mystery.
	const probeCorrelationWindowS = 180
	var rejoins []map[string]any
	for _, inc := range incidents {
		if inc["reason"] == "rejoin" {
			rejoins = append(rejoins, inc)
		}
	}
	if n := len(rejoins); n > 0 {
		precededByProbe := 0
		for _, rj := range rejoins {
			rts := rj["ts"].(float64)
			for _, pts := range probeTimes {
				if pts <= rts && rts-pts <= probeCorrelationWindowS {
					precededByProbe++
					break
				}
			}
		}
		sev := "notice"
		if n >= 3 {
			sev = "warning"
		}
		add(sev, "Rejoin", fmt.Sprintf(
			"%d rejoin event(s) logged (Association/Orphan/re-announce — not just a routine poll) — this device has actually lost and re-established its network connection, not just gone quiet", n))
		if precededByProbe > 0 {
			pct := 100 * precededByProbe / n
			corSev := "notice"
			if pct >= 50 {
				corSev = "warning"
			}
			add(corSev, "Probe correlation", fmt.Sprintf(
				"%d of %d rejoins (%d%%) happened within %ds of an active probe — probing this device appears to nudge it back online, which points at it orphaning (losing its parent silently) rather than a hardware fault; a scheduled probe (Active testing → Schedules) is a reasonable workaround while you chase the root cause",
				precededByProbe, n, pct, probeCorrelationWindowS))
		}
	}

	// Route failures: the strongest, most direct dropout signal — the mesh
	// itself reported it could not reach this device (vs. just being far from
	// the sniffer, which the "Far from sniffer" diagnostic already covers).
	if rf != nil {
		c := rf["count"].(int64)
		reason, _ := rf["last_reason"].(string)
		sev := "notice"
		switch {
		case c >= 10:
			sev = "critical"
		case c >= 3:
			sev = "error"
		case c >= 1:
			sev = "warning"
		}
		add(sev, "Route failures", fmt.Sprintf(
			"%d route failure(s) reported for this device, most recently \"%s\" — the mesh itself lost the path to it",
			c, reason))
	}

	var silences []map[string]any
	for _, inc := range incidents {
		if inc["reason"] == "silence" {
			silences = append(silences, inc)
		}
	}
	if n := len(silences); n > 0 {
		var totalSilent, maxSilent int64
		noisyHits, noisySamples := 0, 0
		for _, s := range silences {
			ss := s["silent_s"].(int64)
			totalSilent += ss
			if ss > maxSilent {
				maxSilent = ss
			}
			if ed := s["ed"].(int64); ed != 0 {
				noisySamples++
				if ed > -75 {
					noisyHits++
				}
			}
		}
		avg := totalSilent / int64(n)
		sev := "notice"
		if n >= 5 {
			sev = "warning"
		}
		pattern := "a repeating pattern, not a one-off"
		if n == 1 {
			pattern = "only one so far — keep watching before drawing conclusions"
		}
		add(sev, "Dropout pattern", fmt.Sprintf(
			"%d silence incident(s) logged, averaging %ds silent (longest %ds) — %s", n, avg, maxSilent, pattern))

		// Trend: are dropouts getting longer or shorter over time? Compare the
		// average of the first half of the history to the second half.
		if n >= 4 {
			half := n / 2
			var firstSum, secondSum int64
			for _, s := range silences[:half] {
				firstSum += s["silent_s"].(int64)
			}
			for _, s := range silences[half:] {
				secondSum += s["silent_s"].(int64)
			}
			firstAvg, secondAvg := firstSum/int64(half), secondSum/int64(n-half)
			switch {
			case secondAvg > firstAvg+10 && float64(secondAvg) > float64(firstAvg)*1.3:
				add("warning", "Trend", fmt.Sprintf(
					"dropouts are getting longer over time (%ds avg early on → %ds avg recently) — worth acting before it goes fully offline",
					firstAvg, secondAvg))
			case firstAvg > 10 && float64(secondAvg) < float64(firstAvg)*0.7:
				add("info", "Trend", fmt.Sprintf(
					"dropouts are getting shorter over time (%ds avg early on → %ds avg recently) — may be self-resolving",
					firstAvg, secondAvg))
			}
		}

		// Channel-noise correlation, from the energy reading captured at each
		// silence incident's detection time.
		if noisySamples >= 2 {
			pct := 100 * noisyHits / noisySamples
			if pct >= 60 {
				add("warning", "Interference", fmt.Sprintf(
					"%d%% of silence incidents (%d/%d) coincided with a busy channel (energy > -75dBm) — RF interference is a plausible cause, not just range",
					pct, noisyHits, noisySamples))
			}
		}

		// Time-of-day clustering — a classic tell for periodic interference
		// (a microwave, a WiFi channel scan, a thermostat cycling nearby)
		// rather than a slow general decline.
		if n >= 4 {
			buckets := map[int]int{}
			for _, s := range silences {
				buckets[time.Unix(int64(s["ts"].(float64)), 0).Hour()/2]++ // 2h buckets
			}
			bestBucket, bestCount := -1, 0
			for b, c := range buckets {
				if c > bestCount {
					bestBucket, bestCount = b, c
				}
			}
			if bestCount*2 >= n {
				add("notice", "Timing", fmt.Sprintf(
					"%d of %d silence incidents happened between %02d:00–%02d:00 — check what runs on a schedule near this device around then",
					bestCount, n, bestBucket*2, bestBucket*2+2))
			}
		}
	}

	// RSSI trend at the sniffer (its vantage point only — not the device's
	// real mesh link, same caveat as Diagnostics' "Far from sniffer").
	if n := len(trend); n >= 10 {
		third := n / 3
		var early, late, earlyN, lateN int64
		for _, t := range trend[:third] {
			early += t["rssi"].(int64)
			earlyN++
		}
		for _, t := range trend[n-third:] {
			late += t["rssi"].(int64)
			lateN++
		}
		if earlyN > 0 && lateN > 0 {
			earlyAvg, lateAvg := early/earlyN, late/lateN
			if earlyAvg-lateAvg >= 8 {
				add("info", "Signal trend", fmt.Sprintf(
					"RSSI at the sniffer has dropped %ddBm over the observed window (%d → %d dBm) — the device may have moved, lost line-of-sight, or something new is blocking the path",
					earlyAvg-lateAvg, earlyAvg, lateAvg))
			}
		}
	}

	if probeTotal > 0 {
		if probeAcks == 0 {
			add("warning", "Active test", fmt.Sprintf(
				"%d active probe(s) sent, 0 acknowledged — the radio isn't answering right now even when directly pinged", probeTotal))
		} else if probeAcks < probeTotal {
			add("info", "Active test", fmt.Sprintf(
				"%d/%d active probes acknowledged — intermittent, consistent with a marginal link rather than a dead radio", probeAcks, probeTotal))
		}
	}

	if len(out) == 0 {
		add("info", "No pattern yet", "not enough history to draw a conclusion — keep the sniffer running, and consider Watch + an active Probe next time it looks unresponsive")
	}
	return out
}

// Diagnostics analyses the captured data and returns a list of health issues,
// each {severity, category, message, addr?}. Severities are syslog levels:
// critical > error > warning > notice > info. ourPan (0 = unknown) is the local
// network's PAN id, used to flag foreign Zigbee networks.
func (d *DB) Diagnostics(ourPan int) []map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []map[string]any
	add := func(sev, cat, msg string, addr int, ts float64) {
		m := map[string]any{"severity": sev, "category": cat, "message": msg}
		if addr >= 0 {
			m["addr"] = fmt.Sprintf("0x%04x", uint16(addr))
		}
		if ts > 0 {
			m["ts"] = ts
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
	// Address conflicts are a distinct, serious signal — a NWK short address
	// was assigned to more than one device, a coordinator-level fault that on
	// its own can produce a device randomly vanishing/reappearing. Reported
	// from a real per-reason lifetime total (route_failure_reasons — NOT
	// route_failures.count, which mixes every route-failure reason together
	// and would overstate this), available immediately with no cold-start
	// problem, rather than clustered below, since concentration-by-time isn't
	// the relevant pattern here.
	q(`SELECT addr,count,last_seen FROM route_failure_reasons WHERE reason='Address conflict' AND count>=1 ORDER BY count DESC LIMIT 15`,
		func(r *sql.Rows) {
			var addr, c int
			var lastSeen float64
			r.Scan(&addr, &c, &lastSeen)
			ago := humanDur(float64(time.Now().Unix()) - lastSeen)
			add("critical", "Address conflict", fmt.Sprintf(
				"%d× NWK short-address conflict, most recently %s ago — the coordinator assigned this address to more than one device at some point, which alone can cause a device to randomly vanish/reappear; re-pair it, or if this shows up on several devices, restart the coordinator's address table",
				c, ago), addr, lastSeen)
		})
	// Every other route failure: root-cause clustering instead of one alert
	// per device — see routeFailureClusterFindings.
	edByCh := map[int]int{}
	q(`SELECT channel, ed_dbm FROM ed_samples WHERE rowid IN (SELECT MAX(rowid) FROM ed_samples GROUP BY channel)`,
		func(r *sql.Rows) {
			var ch, ed int
			r.Scan(&ch, &ed)
			edByCh[ch] = ed
		})
	out = append(out, routeFailureClusterFindings(d.db, edByCh)...)
	// Foreign Zigbee networks — other PAN ids seen (notice). If our PAN is
	// unknown (no ZHA backup), assume the busiest PAN is ours and flag the rest.
	panRow := 0
	q(`SELECT pan,count,channel,last_seen FROM pans ORDER BY count DESC`, func(r *sql.Rows) {
		var pan, c, ch int
		var lastSeen float64
		r.Scan(&pan, &c, &ch, &lastSeen)
		defer func() { panRow++ }()
		mine := (ourPan > 0 && pan == ourPan) || (ourPan == 0 && panRow == 0)
		if mine || c < 3 {
			return
		}
		add("notice", "Foreign network", fmt.Sprintf("PAN 0x%04x seen on ch %d (%d frames) — another Zigbee/Thread network sharing this channel; a possible interference source", uint16(pan), ch, c), -1, lastSeen)
	})
	// Weak links — INFO: this is only the sniffer's vantage, not the mesh link.
	q(`SELECT src,dst,last_lqi,last_seen FROM links WHERE last_lqi < 50 AND count>=2 ORDER BY last_lqi ASC LIMIT 30`,
		func(r *sql.Rows) {
			var src, dst, lqi int
			var lastSeen float64
			r.Scan(&src, &dst, &lqi, &lastSeen)
			add("info", "Weak link", fmt.Sprintf("0x%04x→0x%04x LQI %d as heard by the SNIFFER — may be fine on the mesh; compare the device's Net LQI", uint16(src), uint16(dst), lqi), src, lastSeen)
		})
	// Far from sniffer — INFO (sniffer's distance, not the device's link).
	q(`SELECT addr,last_rssi,last_seen FROM devices WHERE last_rssi < -85 AND count>=2 ORDER BY last_rssi ASC LIMIT 30`,
		func(r *sql.Rows) {
			var addr, rssi int
			var lastSeen float64
			r.Scan(&addr, &rssi, &lastSeen)
			add("info", "Far from sniffer", fmt.Sprintf("RSSI %d dBm at the SNIFFER — the sniffer's distance to the device, not its link to its router. Check Net LQI (HA) or probe from a nearer radio", rssi), addr, lastSeen)
		})
	// Channel noise (latest ED per channel) — warning.
	q(`SELECT channel, ed_dbm, ts FROM ed_samples WHERE rowid IN (SELECT MAX(rowid) FROM ed_samples GROUP BY channel)`,
		func(r *sql.Rows) {
			var ch, ed int
			var ts float64
			r.Scan(&ch, &ed, &ts)
			if ed > -75 {
				add("warning", "Channel noise", fmt.Sprintf("channel %d energy %d dBm — busy; consider a quieter Zigbee channel (15/20/25)", ch, ed), -1, ts)
			}
		})
	now := float64(time.Now().Unix())
	// Silent devices — a device that was chatty then went quiet is the classic
	// dropout signature. Conservative thresholds so sleepy sensors don't spam.
	// Devices whose silences START close together in time (same last_seen
	// neighborhood) share a cause — a capture dropout, interference, or a
	// coordinator hiccup — and get rolled into one finding instead of one row
	// each; same root-cause-over-per-device-alerts reasoning as route
	// failures above.
	type silentDev struct {
		addr int
		ls   float64
		c    int
	}
	var silent []silentDev
	q(`SELECT addr,last_seen,count FROM devices WHERE count>=5 AND addr!=0 ORDER BY last_seen ASC LIMIT 40`,
		func(r *sql.Rows) {
			var sd silentDev
			r.Scan(&sd.addr, &sd.ls, &sd.c)
			if now-sd.ls >= 1800 {
				silent = append(silent, sd)
			}
		})
	const silentClusterGapS = 600 // 10 minutes between last-seens = "went silent together"
	i := 0
	for i < len(silent) {
		j := i + 1
		for j < len(silent) && silent[j].ls-silent[j-1].ls <= silentClusterGapS {
			j++
		}
		group := silent[i:j]
		maxAge := now - group[0].ls
		sev := "notice"
		if maxAge > 7200 {
			sev = "warning"
		}
		if len(group) >= 3 {
			add(sev, "Silent device", fmt.Sprintf(
				"%d devices all went silent within %s of each other, oldest %dm ago — likely a shared cause (capture dropout, interference, or a coordinator hiccup), not %d independent device failures; check those first before chasing individual devices",
				len(group), humanDur(group[len(group)-1].ls-group[0].ls), int(maxAge/60), len(group)), -1, group[len(group)-1].ls)
		} else {
			for _, sd := range group {
				age := now - sd.ls
				s := "notice"
				if age > 7200 {
					s = "warning"
				}
				add(s, "Silent device", fmt.Sprintf("not heard for %dm (was chatty: %d frames) — a candidate for the dropout you're chasing; power-cycle test or active-probe it", int(age/60), sd.c), sd.addr, sd.ls)
			}
		}
		i = j
	}
	// Rejoins — a real Association/Orphan/Coordinator-Realignment/Device-
	// announce, not just a routine poll (see isJoinRelated in main.go). A
	// device that keeps rejoining is losing its network connection and
	// re-establishing it, which is a much stronger and more specific signal
	// than the "Silent device" heuristic above — surface it mesh-wide so a
	// repeat offender doesn't require opening its drawer to notice.
	{
		cutoff := now - 86400
		rows, err := d.db.Query(`SELECT addr, COUNT(*), MAX(ts) FROM incidents
		                         WHERE reason='rejoin' AND ts > ? GROUP BY addr HAVING COUNT(*) >= 2
		                         ORDER BY COUNT(*) DESC LIMIT 20`, cutoff)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var addrHex string
				var c int
				var lastTs float64
				rows.Scan(&addrHex, &c, &lastTs)
				var addr int
				fmt.Sscanf(addrHex, "0x%x", &addr)
				sev := "notice"
				if c >= 5 {
					sev = "warning"
				}
				add(sev, "Rejoin", fmt.Sprintf(
					"%d rejoin events in the last 24h, most recently %s ago — losing and re-establishing its network connection repeatedly, not just going quiet",
					c, humanDur(now-lastTs)), addr, lastTs)
			}
		}
	}
	// Coordinator presence — no 0x0000 traffic usually means wrong channel or a
	// stalled capture.
	var coordSeen float64
	d.db.QueryRow(`SELECT last_seen FROM devices WHERE addr=0`).Scan(&coordSeen)
	if coordSeen == 0 {
		add("warning", "Coordinator", "no coordinator (0x0000) traffic seen — are you on the right channel?", 0, 0)
	} else if now-coordSeen > 300 {
		add("warning", "Coordinator", fmt.Sprintf("no coordinator traffic for %dm — capture may be stalled or off-channel", int((now-coordSeen)/60)), 0, coordSeen)
	}
	// Decryption hint — NWK payloads seen but nothing decrypted (no/incorrect key).
	var pkts, dec int
	d.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(decrypted),0) FROM packets`).Scan(&pkts, &dec)
	if pkts > 200 && dec == 0 {
		add("info", "Decryption", "no frames decrypted — set the network key in Config to decode payloads (addresses still work without it)", -1, 0)
	}
	// HA/zigpy log entries — mesh-wide, so entries about the radio/transport
	// layer itself (no single device involved, e.g. a UART framing error)
	// still surface somewhere, instead of only ever showing up in a specific
	// device's analysis when a "0xXXXX" happens to be present in the message.
	q(`SELECT ts,level,logger,message,count FROM ha_log WHERE level IN ('ERROR','WARNING') ORDER BY ts DESC LIMIT 20`,
		func(r *sql.Rows) {
			var ts float64
			var level, logger, message string
			var count int
			r.Scan(&ts, &level, &logger, &message, &count)
			sev := "warning"
			if level == "ERROR" {
				sev = "error"
			}
			addr := -1
			if m := haLogAddrRe.FindString(message); m != "" {
				var v int
				if _, err := fmt.Sscanf(m, "0x%x", &v); err == nil {
					addr = v
				}
			}
			countNote := ""
			if count > 1 {
				countNote = fmt.Sprintf(" (×%d)", count)
			}
			add(sev, "HA/zigpy log", fmt.Sprintf("%s%s — %s (%s)", message, countNote, humanDur(now-ts)+" ago", logger), addr, ts)
		})
	if len(out) == 0 {
		add("info", "All clear", "no link/route/signal issues detected yet — keep capturing", -1, 0)
	}
	return out
}

// LabelPan tags a PAN with a manufacturer label (e.g. "Philips Hue") derived
// from an OUI seen on that network. Last writer wins so a network with mixed
// silicon settles on the most-recently-seen vendor; the authoritative Hue label
// is applied at read time by the API from the paired bridge's channel.
func (d *DB) LabelPan(pan int, label string) {
	if label == "" || pan < 0 || pan == 0xFFFF {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.db.Exec(`UPDATE pans SET label=? WHERE pan=?`, label, pan)
}

// Networks returns every PAN id seen on-air (yours + foreign), busiest first.
func (d *DB) Networks() []map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, _ := d.db.Query(`SELECT pan,count,channel,last_seen,label FROM pans ORDER BY count DESC`)
	var out []map[string]any
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var pan, count, ch sql.NullInt64
			var ls sql.NullFloat64
			var label sql.NullString
			rows.Scan(&pan, &count, &ch, &ls, &label)
			out = append(out, map[string]any{
				"pan": fmt.Sprintf("0x%04x", uint16(pan.Int64)),
				"count": count.Int64, "channel": ch.Int64, "last_seen": ls.Float64,
				"label": label.String,
			})
		}
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
	rows, _ := d.db.Query(`SELECT ts,radio,channel,rssi,lqi,src,dst,ftype,summary,decrypted,raw
	                       FROM packets ORDER BY id DESC LIMIT ?`, limit)
	var out []map[string]any
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var ts sql.NullFloat64
			var radio, ch, rssi, lqi, dec sql.NullInt64
			var src, dst sql.NullInt64
			var ftype, summary, raw sql.NullString
			rows.Scan(&ts, &radio, &ch, &rssi, &lqi, &src, &dst, &ftype, &summary, &dec, &raw)
			out = append(out, map[string]any{
				"ts": ts.Float64, "radio": radio.Int64, "channel": ch.Int64,
				"rssi": rssi.Int64, "lqi": lqi.Int64, "src": hexAddr(src), "dst": hexAddr(dst),
				"type": ftype.String, "summary": summary.String, "decrypted": dec.Int64 == 1,
				"raw": raw.String,
			})
		}
	}
	return out
}

// histCols maps an RSSI histogram bin index to its pan_minutes column. Column
// names come only from this fixed table (never user input) — the dynamic SQL
// in IngestFrame is safe.
var histCols = [8]string{"h0", "h1", "h2", "h3", "h4", "h5", "h6", "h7"}

// Airtime aggregates the pan_minutes rollup over the last windowS seconds:
// frames/min by PAN (with each PAN's channels and RSSI histogram) plus
// per-channel totals — "who is using the air, and how loudly".
func (d *DB) Airtime(windowS int) map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now().Unix()
	since := int(now)/60 - windowS/60
	type agg struct {
		frames, rssiSum int
		hist            [8]int
		channels        map[int]int
		series          map[int]int
	}
	byPan := map[int]*agg{}
	chTotals := map[int]int{}
	rows, _ := d.db.Query(`SELECT bucket,pan,channel,frames,rssi_sum,
	 h0,h1,h2,h3,h4,h5,h6,h7 FROM pan_minutes WHERE bucket>=?`, since)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var bucket, pan, ch, frames, rssiSum int
			var h [8]int
			rows.Scan(&bucket, &pan, &ch, &frames, &rssiSum,
				&h[0], &h[1], &h[2], &h[3], &h[4], &h[5], &h[6], &h[7])
			a := byPan[pan]
			if a == nil {
				a = &agg{channels: map[int]int{}, series: map[int]int{}}
				byPan[pan] = a
			}
			a.frames += frames
			a.rssiSum += rssiSum
			for i := range h {
				a.hist[i] += h[i]
			}
			a.channels[ch] += frames
			a.series[bucket] += frames
			chTotals[ch] += frames
		}
	}
	labels := map[int]string{}
	if lr, err := d.db.Query(`SELECT pan,label FROM pans WHERE label IS NOT NULL AND label!=''`); err == nil {
		for lr.Next() {
			var pan int
			var label string
			lr.Scan(&pan, &label)
			labels[pan] = label
		}
		lr.Close()
	}
	var pansOut []map[string]any
	for pan, a := range byPan {
		chans := make([]int, 0, len(a.channels))
		for c := range a.channels {
			chans = append(chans, c)
		}
		sort.Slice(chans, func(i, j int) bool { return a.channels[chans[i]] > a.channels[chans[j]] })
		buckets := make([]int, 0, len(a.series))
		for b := range a.series {
			buckets = append(buckets, b)
		}
		sort.Ints(buckets)
		series := make([][2]int, len(buckets))
		for i, b := range buckets {
			series[i] = [2]int{b, a.series[b]}
		}
		panHex := "(none)"
		if pan >= 0 {
			panHex = fmt.Sprintf("0x%04x", uint16(pan))
		}
		pansOut = append(pansOut, map[string]any{
			"pan": panHex, "pan_int": pan, "label": labels[pan],
			"frames": a.frames, "rssi_avg": a.rssiSum / max(a.frames, 1),
			"channels": chans, "hist": a.hist, "series": series,
		})
	}
	sort.Slice(pansOut, func(i, j int) bool { return pansOut[i]["frames"].(int) > pansOut[j]["frames"].(int) })
	chansOut := []map[string]any{}
	for c := 11; c <= 26; c++ {
		if chTotals[c] > 0 {
			chansOut = append(chansOut, map[string]any{"channel": c, "frames": chTotals[c]})
		}
	}
	return map[string]any{
		"window_s": windowS, "bucket_s": 60, "now": now,
		"pans": pansOut, "channels": chansOut,
	}
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
