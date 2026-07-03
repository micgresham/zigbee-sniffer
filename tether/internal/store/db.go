// Package store persists packets, devices, links, ED samples, and incidents in
// SQLite (pure-Go driver, no cgo). Port of host/zbsniff/store/db.py.
package store

import (
	"database/sql"
	"encoding/hex"
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
CREATE INDEX IF NOT EXISTS idx_ed_ts ON ed_samples(ts);
CREATE INDEX IF NOT EXISTS idx_ed_channel ON ed_samples(channel);
CREATE INDEX IF NOT EXISTS idx_packets_ts ON packets(ts);
CREATE TABLE IF NOT EXISTS incidents(
 id INTEGER PRIMARY KEY AUTOINCREMENT, ts REAL, addr TEXT, reason TEXT,
 rssi INT, lqi INT, channel INT, ed INT, silent_s INT, raw TEXT);
CREATE TABLE IF NOT EXISTS route_failures(addr INTEGER PRIMARY KEY, count INT, last_reason TEXT, last_seen REAL);
CREATE TABLE IF NOT EXISTS pans(pan INTEGER PRIMARY KEY, count INT, last_seen REAL, channel INT, label TEXT);
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
	// One-time trim: an existing DB may already hold millions of ed_samples from
	// before the cap existed. Bring it back under edCap so queries are fast now.
	d.Exec(`DELETE FROM ed_samples WHERE rowid <= (SELECT MAX(rowid) - ? FROM ed_samples)`, edCap)
	return &DB{db: d}, nil
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
	// MAC-layer addresses are the physical hop (often router<->coordinator).
	if src >= 0 && src != broadcast {
		d.upsertDevice(int(src), ts, rssi, lqi, channel, panInt)
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
			d.upsertDevice(ns, ts, rssi, lqi, channel, panInt)
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
// each {severity, category, message, addr?}. Severities are syslog levels:
// critical > error > warning > notice > info. ourPan (0 = unknown) is the local
// network's PAN id, used to flag foreign Zigbee networks.
func (d *DB) Diagnostics(ourPan int) []map[string]any {
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
	// Route failures (the strongest dropout signal) — a real error.
	q(`SELECT addr,count,last_reason FROM route_failures WHERE count>=2 ORDER BY count DESC LIMIT 30`,
		func(r *sql.Rows) {
			var addr, c int
			var reason string
			r.Scan(&addr, &c, &reason)
			sev := "error"
			if c >= 10 {
				sev = "critical"
			}
			add(sev, "Route failure", fmt.Sprintf("%d× \"%s\" — device hard to reach (re-pair or add a router nearby)", c, reason), addr)
		})
	// Foreign Zigbee networks — other PAN ids seen (notice). If our PAN is
	// unknown (no ZHA backup), assume the busiest PAN is ours and flag the rest.
	panRow := 0
	q(`SELECT pan,count,channel FROM pans ORDER BY count DESC`, func(r *sql.Rows) {
		var pan, c, ch int
		r.Scan(&pan, &c, &ch)
		defer func() { panRow++ }()
		mine := (ourPan > 0 && pan == ourPan) || (ourPan == 0 && panRow == 0)
		if mine || c < 3 {
			return
		}
		add("notice", "Foreign network", fmt.Sprintf("PAN 0x%04x seen on ch %d (%d frames) — another Zigbee/Thread network sharing this channel; a possible interference source", uint16(pan), ch, c), -1)
	})
	// Weak links — INFO: this is only the sniffer's vantage, not the mesh link.
	q(`SELECT src,dst,last_lqi FROM links WHERE last_lqi < 50 AND count>=2 ORDER BY last_lqi ASC LIMIT 30`,
		func(r *sql.Rows) {
			var src, dst, lqi int
			r.Scan(&src, &dst, &lqi)
			add("info", "Weak link", fmt.Sprintf("0x%04x→0x%04x LQI %d as heard by the SNIFFER — may be fine on the mesh; compare the device's Net LQI", uint16(src), uint16(dst), lqi), src)
		})
	// Far from sniffer — INFO (sniffer's distance, not the device's link).
	q(`SELECT addr,last_rssi FROM devices WHERE last_rssi < -85 AND count>=2 ORDER BY last_rssi ASC LIMIT 30`,
		func(r *sql.Rows) {
			var addr, rssi int
			r.Scan(&addr, &rssi)
			add("info", "Far from sniffer", fmt.Sprintf("RSSI %d dBm at the SNIFFER — the sniffer's distance to the device, not its link to its router. Check Net LQI (HA) or probe from a nearer radio", rssi), addr)
		})
	// Channel noise (latest ED per channel) — warning.
	q(`SELECT channel, ed_dbm FROM ed_samples WHERE rowid IN (SELECT MAX(rowid) FROM ed_samples GROUP BY channel)`,
		func(r *sql.Rows) {
			var ch, ed int
			r.Scan(&ch, &ed)
			if ed > -75 {
				add("warning", "Channel noise", fmt.Sprintf("channel %d energy %d dBm — busy; consider a quieter Zigbee channel (15/20/25)", ch, ed), -1)
			}
		})
	now := float64(time.Now().Unix())
	// Silent devices — a device that was chatty then went quiet is the classic
	// dropout signature. Conservative thresholds so sleepy sensors don't spam.
	q(`SELECT addr,last_seen,count FROM devices WHERE count>=5 AND addr!=0 ORDER BY last_seen ASC LIMIT 40`,
		func(r *sql.Rows) {
			var addr, c int
			var ls float64
			r.Scan(&addr, &ls, &c)
			age := now - ls
			if age < 1800 {
				return
			}
			sev := "notice"
			if age > 7200 {
				sev = "warning"
			}
			add(sev, "Silent device", fmt.Sprintf("not heard for %dm (was chatty: %d frames) — a candidate for the dropout you're chasing; power-cycle test or active-probe it", int(age/60), c), addr)
		})
	// Coordinator presence — no 0x0000 traffic usually means wrong channel or a
	// stalled capture.
	var coordSeen float64
	d.db.QueryRow(`SELECT last_seen FROM devices WHERE addr=0`).Scan(&coordSeen)
	if coordSeen == 0 {
		add("warning", "Coordinator", "no coordinator (0x0000) traffic seen — are you on the right channel?", 0)
	} else if now-coordSeen > 300 {
		add("warning", "Coordinator", fmt.Sprintf("no coordinator traffic for %dm — capture may be stalled or off-channel", int((now-coordSeen)/60)), 0)
	}
	// Decryption hint — NWK payloads seen but nothing decrypted (no/incorrect key).
	var pkts, dec int
	d.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(decrypted),0) FROM packets`).Scan(&pkts, &dec)
	if pkts > 200 && dec == 0 {
		add("info", "Decryption", "no frames decrypted — set the network key in Config to decode payloads (addresses still work without it)", -1)
	}
	if len(out) == 0 {
		add("info", "All clear", "no link/route/signal issues detected yet — keep capturing", -1)
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
