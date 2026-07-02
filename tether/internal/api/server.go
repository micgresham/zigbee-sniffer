// Package api serves the REST + WebSocket API and the embedded web UI.
package api

import (
	"context"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"zbsniff/internal/config"
	"zbsniff/internal/logbuf"
	"zbsniff/internal/names"
	"zbsniff/internal/proto"
	"zbsniff/internal/radios"
	"zbsniff/internal/runtime"
	"zbsniff/internal/stats"
	"zbsniff/internal/store"
)

// panOf returns the runtime PAN id as an int (0 if unknown).
func panOf(rt *runtime.Runtime) int {
	if rt == nil {
		return 0
	}
	return int(rt.Pan())
}

// statusChannel reads the device's current channel from the latest status.
func statusChannel(rt *runtime.Runtime) int {
	if rt == nil {
		return 0
	}
	switch n := rt.Status()["channel"].(type) {
	case int:
		return n
	case uint8:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// panHex renders the runtime PAN id as "0x1234" (or "" if unknown).
func panHex(rt *runtime.Runtime) string {
	if rt == nil || rt.Pan() == 0 {
		return ""
	}
	return fmt.Sprintf("0x%04x", rt.Pan())
}

// parseAddr parses a "0x1234" / "1234" short address.
func parseAddr(s string) (int, bool) {
	v, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "0x"), 16, 16)
	if err != nil {
		return 0, false
	}
	return int(v), true
}

// CommandSink sends a command frame to the device(s).
type CommandSink func(data []byte)

// Hub fans out live events to WebSocket subscribers.
type Hub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

// NewHub creates an event hub.
func NewHub() *Hub { return &Hub{subs: map[chan []byte]struct{}{}} }

func (h *Hub) sub() chan []byte {
	c := make(chan []byte, 1000)
	h.mu.Lock()
	h.subs[c] = struct{}{}
	h.mu.Unlock()
	return c
}
func (h *Hub) unsub(c chan []byte) {
	h.mu.Lock()
	delete(h.subs, c)
	h.mu.Unlock()
}

// Publish sends an event (will be JSON-encoded) to all subscribers.
func (h *Hub) Publish(event any) {
	b, err := json.Marshal(event)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.subs {
		select {
		case c <- b:
		default:
		}
	}
}

// Server holds API dependencies.
type Server struct {
	DB   *store.DB
	Hub  *Hub
	Send CommandSink
	Reg  *names.Registry  // optional name resolution (ZHA backup / HA API)
	RT    *runtime.Runtime // mutable key + latest device status
	HA    *names.HAManager // runtime HA connection
	Hue   *names.HueManager // runtime Philips Hue bridge connection
	Stats *stats.Stats     // connection/ingest counters
	Logs  *logbuf.Buf      // host log ring
	Web   []byte           // embedded index.html

	SendTo func(port string, b []byte) bool // per-radio command send ("" = broadcast)
	Radios *radios.Tracker                  // connected dongle identities

	SaveConfig func(func(*config.Config)) // persist settings to the config file
	SetRole    func(radioID int, role string) // assign a radio's function
	Roles      func() map[int]string          // current radio-id → role map
	Reconnect  func()                          // force-reinit the serial link
	About      map[string]any                  // version/build metadata for the About tab
	Prefs      func() map[string]string        // current web-UI preferences (from config)
	Silence    func() int                      // current incident silence threshold (seconds)
}

// incidentSilenceDefault mirrors the detector's default when unset.
const incidentSilenceDefault = 120

// writeCSV writes rows as CSV with a stable, sorted column header.
func writeCSV(w io.Writer, rows []map[string]any) {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	keyset := map[string]bool{}
	for _, r := range rows {
		for k := range r {
			keyset[k] = true
		}
	}
	keys := make([]string, 0, len(keyset))
	for k := range keyset {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return
	}
	cw.Write(keys)
	for _, r := range rows {
		rec := make([]string, len(keys))
		for i, k := range keys {
			if v, ok := r[k]; ok && v != nil {
				rec[i] = fmt.Sprint(v)
			}
		}
		cw.Write(rec)
	}
}

// allocate picks a radio for a function ("capture","spectrum","probe"): a radio
// dedicated to that role first, else any "any"/unassigned radio (preferring a
// free one), else a piggyback for probes. interrupt=true means the chosen radio
// is mid-task and running fn disrupts it; ok=false means none is free and reason
// lists what each radio is doing.
func (s *Server) allocate(fn string) (port string, radioID int, ok, interrupt bool, reason string) {
	if s.Radios == nil {
		return "", -1, true, false, ""
	}
	list := s.Radios.List()
	if len(list) == 0 {
		return "", -1, true, false, "" // nothing tracked yet → let it through (broadcast)
	}
	roles := map[int]string{}
	if s.Roles != nil {
		roles = s.Roles()
	}
	roleOf := func(id int) string {
		if r := roles[id]; r != "" {
			return r
		}
		return "any"
	}
	dedicated := map[string]string{"spectrum": "spectrum", "capture": "sniffer", "probe": "tester"}[fn]
	capturing := func(r *radios.Radio) bool { return (r.Mode == 1 || r.Mode == 3) && !r.Stopped }
	conflicts := func(r *radios.Radio) bool {
		switch fn {
		case "spectrum":
			return capturing(r)
		case "capture":
			return r.Mode == 2
		}
		return false // probes piggyback on capture
	}
	for _, r := range list { // 1) dedicated role
		if roleOf(r.RadioID) == dedicated {
			return r.Port, r.RadioID, true, false, ""
		}
	}
	var intr *radios.Radio // 2) an "any" radio (prefer free)
	for _, r := range list {
		if roleOf(r.RadioID) == "any" {
			if !conflicts(r) {
				return r.Port, r.RadioID, true, false, ""
			}
			if intr == nil {
				intr = r
			}
		}
	}
	if intr != nil {
		return intr.Port, intr.RadioID, true, true, fmt.Sprintf(
			"Radio %d is %s. Running %s on it will interrupt that — a single radio can't do both. Continue?",
			intr.RadioID, activity(intr), fn)
	}
	if fn == "probe" { // 3) piggyback probe on a capturing radio
		for _, r := range list {
			if capturing(r) {
				return r.Port, r.RadioID, true, false, ""
			}
		}
	}
	return "", -1, false, false, "No radio is free for " + fn + ". In use: " + describeInUse(list, roleOf)
}

func activity(r *radios.Radio) string {
	if r.Stopped {
		return "stopped"
	}
	switch r.Mode {
	case 1, 3:
		return "capturing"
	case 2:
		return "running an ED sweep"
	case 0:
		return "idle"
	}
	return "busy"
}

func describeInUse(list []*radios.Radio, roleOf func(int) string) string {
	parts := make([]string, 0, len(list))
	for _, r := range list {
		parts = append(parts, fmt.Sprintf("radio %d = %s (role: %s)", r.RadioID, activity(r), roleOf(r.RadioID)))
	}
	return strings.Join(parts, ", ")
}

// probe dispatches one active MAC probe (pan 0 → use the runtime's PAN).
// With no explicit port, it prefers a radio assigned the "tester" role.
func (s *Server) probe(target, pan int, port string) bool {
	if s.SendTo == nil {
		return false
	}
	if pan == 0 && s.RT != nil {
		pan = int(s.RT.Pan())
	}
	if port == "" {
		if p, _, ok, _, _ := s.allocate("probe"); ok {
			port = p
		}
	}
	ok := s.SendTo(port, proto.CmdProbeMsg(uint16(target), uint16(pan)))
	if ok && s.Radios != nil && port != "" {
		s.Radios.Probed(port)
	}
	return ok
}

// sendFnTo sends frames to a specific port, or broadcasts when port is "".
func (s *Server) sendFnTo(port string, frames ...[]byte) {
	if s.SendTo != nil && port != "" {
		for _, f := range frames {
			s.SendTo(port, f)
		}
		return
	}
	s.cmd(frames...)
}

// runSurvey hops every Zigbee channel, capturing briefly on each so foreign
// networks on other channels get recorded in the pans table. When active is set
// it also transmits a beacon request per channel, soliciting replies from even
// idle networks. It publishes per-channel progress on the WebSocket, then
// restores the original channel.
func (s *Server) runSurvey(port string, dwellMs int, active bool) {
	orig := statusChannel(s.RT)
	for ch := int(proto.ChannelMin); ch <= int(proto.ChannelMax); ch++ {
		s.sendFnTo(port,
			proto.CmdSetChannelMsg(byte(ch)),
			proto.CmdSetModeMsg(proto.ModeCapture),
			proto.CmdStartMsg())
		s.Hub.Publish(map[string]any{"kind": "survey", "channel": ch, "active": active, "done": false})
		if active {
			// Let the radio settle on the new channel, then solicit beacons.
			// Send twice — a single request can be lost on a busy channel.
			time.Sleep(150 * time.Millisecond)
			s.sendFnTo(port, proto.CmdBeaconReqMsg())
			time.Sleep(200 * time.Millisecond)
			s.sendFnTo(port, proto.CmdBeaconReqMsg())
			time.Sleep(time.Duration(dwellMs) * time.Millisecond)
		} else {
			time.Sleep(time.Duration(dwellMs) * time.Millisecond)
		}
	}
	if orig >= int(proto.ChannelMin) && orig <= int(proto.ChannelMax) {
		s.sendFnTo(port,
			proto.CmdSetChannelMsg(byte(orig)),
			proto.CmdSetModeMsg(proto.ModeCapture),
			proto.CmdStartMsg())
	}
	s.Hub.Publish(map[string]any{"kind": "survey", "channel": orig, "active": active, "done": true})
}

// cmd sends one or more framed command messages to the device(s).
func (s *Server) cmd(frames ...[]byte) {
	if s.Send == nil {
		return
	}
	for _, f := range frames {
		s.Send(f)
	}
}

// enrich fills name + network-reported signal (from HA) onto device rows. The
// sniffer's own rssi/lqi stay as-is; net_lqi/net_rssi are the coordinator's view.
func (s *Server) enrich(rows []map[string]any) []map[string]any {
	if s.Reg == nil {
		return rows
	}
	for _, r := range rows {
		addr, ok := r["addr"].(string)
		if !ok {
			continue
		}
		if r["name"] == "" || r["name"] == nil {
			r["name"] = s.Reg.Name(addr)
		}
		if net, ok := s.Reg.Net(addr); ok {
			if net.HasLQI {
				r["net_lqi"] = net.LQI
			}
			if net.HasRSSI {
				r["net_rssi"] = net.RSSI
			}
		}
	}
	return rows
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// Handler builds the HTTP mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "ok", "devices": len(s.DB.Devices())})
	})
	mux.HandleFunc("/api/devices", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.enrich(nz(s.DB.Devices())))
	})
	mux.HandleFunc("/api/devices/{addr}/messages", func(w http.ResponseWriter, r *http.Request) {
		v, err := strconv.ParseUint(strings.TrimPrefix(r.PathValue("addr"), "0x"), 16, 16)
		if err != nil {
			writeJSON(w, []any{})
			return
		}
		writeJSON(w, nz(s.DB.Messages(int(v), 60)))
	})
	mux.HandleFunc("/api/routing", func(w http.ResponseWriter, r *http.Request) {
		rt := s.DB.Routing()
		if nodes, ok := rt["nodes"].([]map[string]any); ok {
			s.enrich(nodes)
		}
		writeJSON(w, rt)
	})
	mux.HandleFunc("/api/incidents", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, nz(s.DB.Incidents(200)))
	})
	mux.HandleFunc("/api/diagnostics", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, nz(s.DB.Diagnostics(panOf(s.RT))))
	})
	mux.HandleFunc("/api/stats", func(w http.ResponseWriter, r *http.Request) {
		if s.Stats != nil {
			writeJSON(w, s.Stats.Snapshot())
			return
		}
		writeJSON(w, map[string]any{})
	})
	// Recent captured frames (seeds the live-frames table on page load, since the
	// WebSocket only streams frames that arrive after the page connects).
	mux.HandleFunc("/api/frames", func(w http.ResponseWriter, r *http.Request) {
		limit := 200
		if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
			limit = n
		}
		writeJSON(w, nz(s.DB.Packets(limit)))
	})
	mux.HandleFunc("/api/about", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.About)
	})
	// Web-UI preferences persisted to the config YAML. GET returns them all;
	// POST ?key=&value= saves one.
	mux.HandleFunc("/api/prefs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			key := r.URL.Query().Get("key")
			val := r.URL.Query().Get("value")
			if key != "" && s.SaveConfig != nil {
				s.SaveConfig(func(c *config.Config) {
					if c.UIPrefs == nil {
						c.UIPrefs = map[string]string{}
					}
					c.UIPrefs[key] = val
				})
			}
			writeJSON(w, map[string]any{"ok": true})
			return
		}
		p := map[string]string{}
		if s.Prefs != nil {
			p = s.Prefs()
		}
		writeJSON(w, p)
	})
	// Firmware OTA: POST the .bin as the request body. target 0 = this C6,
	// 1..3 = a satellite (relayed over SPI). Streams chunks in the background;
	// progress arrives on the WebSocket as {kind:"ota",...}.
	mux.HandleFunc("/api/ota", func(w http.ResponseWriter, r *http.Request) {
		target, _ := strconv.Atoi(r.URL.Query().Get("target"))
		port := r.URL.Query().Get("port")
		data, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		r.Body.Close()
		if err != nil || len(data) < 1024 {
			writeJSON(w, map[string]any{"ok": false, "reason": "empty or too-small firmware upload"})
			return
		}
		crc := crc32.ChecksumIEEE(data)
		send := func(b []byte) {
			if port != "" {
				s.SendTo(port, b)
			} else {
				s.cmd(b)
			}
		}
		go func() {
			send(proto.CmdOtaBeginMsg(byte(target), uint32(len(data)), crc))
			time.Sleep(400 * time.Millisecond) // let esp_ota_begin() erase/prepare
			// Satellite chunks are relayed over SPI (256-byte transactions), so
			// keep them small enough to fit one transaction incl. framing.
			chunk := 512
			if target != 0 {
				chunk = 200
			}
			for off := 0; off < len(data); off += chunk {
				end := off + chunk
				if end > len(data) {
					end = len(data)
				}
				send(proto.CmdOtaDataMsg(byte(target), uint32(off), data[off:end]))
				time.Sleep(8 * time.Millisecond) // pace flash writes
			}
			send(proto.CmdOtaEndMsg(byte(target)))
		}()
		writeJSON(w, map[string]any{"ok": true, "size": len(data), "crc": fmt.Sprintf("%08x", crc)})
	})
	// Force a serial reconnect (drops the current link; the host reconnects).
	mux.HandleFunc("/api/reconnect", func(w http.ResponseWriter, r *http.Request) {
		if s.Reconnect != nil {
			s.Reconnect()
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		if s.Logs != nil {
			writeJSON(w, s.Logs.Lines())
			return
		}
		writeJSON(w, []string{})
	})
	// Export collected data as CSV (default) or JSON.
	mux.HandleFunc("/api/export", func(w http.ResponseWriter, r *http.Request) {
		what := r.URL.Query().Get("what")
		var rows []map[string]any
		switch what {
		case "devices":
			rows = s.enrich(s.DB.Devices())
		case "links":
			if e, ok := s.DB.Routing()["edges"].([]map[string]any); ok {
				rows = e
			}
		case "incidents":
			rows = s.DB.Incidents(100000)
		case "frames", "packets":
			rows = s.DB.Packets(20000)
		case "diagnostics":
			rows = s.DB.Diagnostics(panOf(s.RT))
		case "spectrum":
			rows = s.DB.Spectrum()
		default:
			http.Error(w, "unknown export 'what'", 400)
			return
		}
		ts := time.Now().Format("20060102-150405")
		if r.URL.Query().Get("format") == "json" {
			w.Header().Set("Content-Disposition", "attachment; filename=zbsniff-"+what+"-"+ts+".json")
			writeJSON(w, nz(rows))
			return
		}
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", "attachment; filename=zbsniff-"+what+"-"+ts+".csv")
		writeCSV(w, rows)
	})
	// --- active testing & radios ---
	mux.HandleFunc("/api/radios", func(w http.ResponseWriter, r *http.Request) {
		var list any = []any{}
		if s.Radios != nil {
			radios := s.Radios.List()
			var roleMap map[int]string
			if s.Roles != nil {
				roleMap = s.Roles()
			}
			for _, rd := range radios {
				if rd.RadioID >= 0 && roleMap != nil {
					rd.Role = roleMap[rd.RadioID]
				}
			}
			list = radios
		}
		writeJSON(w, map[string]any{"radios": list, "pan": panHex(s.RT),
			"roles": []string{"any", "sniffer", "spectrum", "tester", "hopper", "idle"}})
	})
	// Assign a radio (by radio-id) to a function; the host applies + persists it.
	mux.HandleFunc("/api/radio_role", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.URL.Query().Get("radio"))
		if err != nil {
			http.Error(w, "bad radio id", 400)
			return
		}
		if s.SetRole != nil {
			s.SetRole(id, r.URL.Query().Get("role"))
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	// Provision a dongle's radio-id (persisted to its NVS) so roles can target it.
	mux.HandleFunc("/api/set_radio_id", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.URL.Query().Get("id"))
		port := r.URL.Query().Get("port")
		if err != nil || port == "" {
			http.Error(w, "need port and id", 400)
			return
		}
		if s.SendTo != nil {
			s.SendTo(port, proto.CmdSetRadioIDMsg(byte(id)))
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/probe", func(w http.ResponseWriter, r *http.Request) {
		addr, ok := parseAddr(r.URL.Query().Get("addr"))
		if !ok {
			http.Error(w, "bad addr", 400)
			return
		}
		pan, _ := parseAddr(r.URL.Query().Get("pan")) // 0 if absent → runtime PAN
		port := r.URL.Query().Get("port")
		sent := s.probe(addr, pan, port)
		writeJSON(w, map[string]any{"sent": sent})
	})
	mux.HandleFunc("/api/probe_history", func(w http.ResponseWriter, r *http.Request) {
		target := -1
		if a, ok := parseAddr(r.URL.Query().Get("addr")); ok {
			target = a
		}
		writeJSON(w, map[string]any{
			"history": nz(s.DB.ProbeHistory(target, 500)),
			"summary": s.DB.ProbeSummary(),
		})
	})
	mux.HandleFunc("/api/schedules", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			addr, ok := parseAddr(r.URL.Query().Get("addr"))
			if !ok {
				http.Error(w, "bad addr", 400)
				return
			}
			interval, _ := strconv.Atoi(r.URL.Query().Get("interval"))
			if interval < 5 {
				interval = 60
			}
			pan, _ := parseAddr(r.URL.Query().Get("pan"))
			id := s.DB.AddSchedule(addr, pan, r.URL.Query().Get("port"), interval)
			writeJSON(w, map[string]any{"id": id})
		case http.MethodDelete:
			if id, err := strconv.Atoi(r.URL.Query().Get("id")); err == nil {
				s.DB.DeleteSchedule(id)
			}
			writeJSON(w, map[string]any{"ok": true})
		default:
			writeJSON(w, map[string]any{"schedules": s.DB.Schedules()})
		}
	})
	mux.HandleFunc("/api/schedule_toggle", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.URL.Query().Get("id"))
		if err != nil {
			http.Error(w, "bad id", 400)
			return
		}
		s.DB.SetScheduleEnabled(id, r.URL.Query().Get("on") == "1")
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/spectrum", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, nz(s.DB.Spectrum()))
	})
	// Zigbee networks (PAN ids) seen on-air, and which one is ours.
	mux.HandleFunc("/api/networks", func(w http.ResponseWriter, r *http.Request) {
		nets := nz(s.DB.Networks())
		ours := panHex(s.RT)
		// A paired Hue bridge reports its own Zigbee channel. Any FOREIGN network
		// on that channel is almost certainly the Hue network, so label it even if
		// we never caught a Hue device's extended address on-air.
		if s.Hue != nil {
			if hueCh, ok := s.Hue.Status()["channel"].(int); ok && hueCh > 0 {
				for _, n := range nets {
					pan, _ := n["pan"].(string)
					lbl, _ := n["label"].(string)
					ch, _ := n["channel"].(int64)
					if pan != ours && lbl == "" && int(ch) == hueCh {
						n["label"] = "Philips Hue (bridge)"
					}
				}
			}
		}
		writeJSON(w, map[string]any{"networks": nets, "ours": ours})
	})
	// Full-band survey: hop every channel briefly to discover networks on all of
	// them (single radio → pauses capture). Progress goes out on the WebSocket.
	mux.HandleFunc("/api/survey", func(w http.ResponseWriter, r *http.Request) {
		port, _, ok, _, reason := s.allocate("spectrum")
		if !ok {
			writeJSON(w, map[string]any{"ok": false, "reason": reason})
			return
		}
		dwell := 1500
		if d, err := strconv.Atoi(r.URL.Query().Get("dwell")); err == nil && d >= 300 {
			dwell = d
		}
		active := r.URL.Query().Get("active") == "1"
		go s.runSurvey(port, dwell, active)
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/set_channel", func(w http.ResponseWriter, r *http.Request) {
		ch, _ := strconv.Atoi(r.URL.Query().Get("ch"))
		if ch >= proto.ChannelMin && ch <= proto.ChannelMax && s.Send != nil {
			s.Send(proto.CmdSetChannelMsg(byte(ch)))
			s.Send(proto.CmdSetModeMsg(proto.ModeCapture))
			s.Send(proto.CmdStartMsg())
			if s.SaveConfig != nil {
				s.SaveConfig(func(c *config.Config) { c.Channel = ch })
			}
		}
		writeJSON(w, map[string]any{"channel": ch})
	})
	// Dry-run: which radio would handle a function, or why none can.
	mux.HandleFunc("/api/allocate", func(w http.ResponseWriter, r *http.Request) {
		port, id, ok, interrupt, reason := s.allocate(r.URL.Query().Get("fn"))
		writeJSON(w, map[string]any{"ok": ok, "port": port, "radio": id,
			"interrupt": interrupt, "reason": reason})
	})
	mux.HandleFunc("/api/scan", func(w http.ResponseWriter, r *http.Request) {
		// One-shot energy-detect sweep of all channels; dwell ms/channel.
		dwell, _ := strconv.Atoi(r.URL.Query().Get("dwell"))
		if dwell <= 0 {
			dwell = 5
		}
		port, _, ok, _, reason := s.allocate("spectrum")
		if !ok {
			writeJSON(w, map[string]any{"ok": false, "reason": reason})
			return
		}
		s.sendFnTo(port, proto.CmdEdScanMsg(proto.AllChannelsMask(), uint16(dwell)))
		writeJSON(w, map[string]any{"ok": true, "scanning": true, "dwell": dwell})
	})
	mux.HandleFunc("/api/start", func(w http.ResponseWriter, r *http.Request) {
		port, _, ok, _, reason := s.allocate("capture")
		if !ok {
			writeJSON(w, map[string]any{"ok": false, "reason": reason})
			return
		}
		s.sendFnTo(port, proto.CmdSetModeMsg(proto.ModeCapture), proto.CmdStartMsg())
		if s.Radios != nil {
			s.Radios.SetStopped(port, false)
		}
		writeJSON(w, map[string]any{"ok": true, "capturing": true})
	})
	mux.HandleFunc("/api/stop", func(w http.ResponseWriter, r *http.Request) {
		s.cmd(proto.CmdStopMsg())
		if s.Radios != nil {
			s.Radios.SetStopped("", true) // all radios
		}
		writeJSON(w, map[string]any{"stopped": true})
	})
	mux.HandleFunc("/api/mode", func(w http.ResponseWriter, r *http.Request) {
		m, _ := strconv.Atoi(r.URL.Query().Get("m"))
		fn := ""
		switch m {
		case proto.ModeEdSweep:
			fn = "spectrum"
		case proto.ModeCapture, proto.ModeCapturePlusEd:
			fn = "capture"
		}
		port := ""
		if fn != "" {
			p, _, ok, _, reason := s.allocate(fn)
			if !ok {
				writeJSON(w, map[string]any{"ok": false, "reason": reason})
				return
			}
			port = p
		}
		frames := [][]byte{proto.CmdSetModeMsg(byte(m))}
		if m == proto.ModeCapture {
			frames = append(frames, proto.CmdStartMsg())
		}
		s.sendFnTo(port, frames...)
		if s.SaveConfig != nil {
			s.SaveConfig(func(c *config.Config) { c.Mode = m })
		}
		writeJSON(w, map[string]any{"ok": true, "mode": m})
	})
	// Incident detector threshold: GET returns the current + default silence
	// window (seconds); POST ?silence=N persists a new one.
	mux.HandleFunc("/api/incident_config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if n, err := strconv.Atoi(r.URL.Query().Get("silence")); err == nil && n >= 10 && s.SaveConfig != nil {
				s.SaveConfig(func(c *config.Config) { c.IncidentSilenceS = n })
			}
		}
		cur := 0
		if s.Silence != nil {
			cur = s.Silence()
		}
		if cur <= 0 {
			cur = incidentSilenceDefault
		}
		writeJSON(w, map[string]any{"silence_s": cur, "default": incidentSilenceDefault})
	})
	mux.HandleFunc("/api/hop", func(w http.ResponseWriter, r *http.Request) {
		dwell, _ := strconv.Atoi(r.URL.Query().Get("dwell"))
		var mask uint32
		if dwell > 0 {
			mask = proto.AllChannelsMask() // hop across 11-26
		}
		s.cmd(proto.CmdSetHopMsg(mask, uint16(dwell)))
		if s.SaveConfig != nil {
			s.SaveConfig(func(c *config.Config) { c.HopDwellMs = dwell })
		}
		writeJSON(w, map[string]any{"hop_dwell_ms": dwell})
	})
	mux.HandleFunc("/api/reset_radio", func(w http.ResponseWriter, r *http.Request) {
		// Stop then re-arm the receiver (a soft radio reset).
		s.cmd(proto.CmdStopMsg())
		time.Sleep(150 * time.Millisecond)
		s.cmd(proto.CmdSetModeMsg(proto.ModeCapture), proto.CmdStartMsg())
		writeJSON(w, map[string]any{"reset": true})
	})
	mux.HandleFunc("/api/key", func(w http.ResponseWriter, r *http.Request) {
		// Set/clear the host-side decryption key live (no device round-trip).
		clean := strings.NewReplacer(":", "", "-", "", " ", "", "0x", "").Replace(r.URL.Query().Get("k"))
		if s.RT != nil {
			if clean == "" {
				s.RT.SetKey(nil)
			} else if k, err := hex.DecodeString(clean); err == nil {
				s.RT.SetKey(k)
			}
			if s.SaveConfig != nil {
				s.SaveConfig(func(c *config.Config) { c.Key = clean })
			}
		}
		writeJSON(w, map[string]any{"decrypt": s.RT != nil && s.RT.HasKey()})
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		if s.RT != nil {
			s.cmd(proto.CmdGetStatusMsg()) // nudge the device to report
			writeJSON(w, s.RT.Status())
			return
		}
		writeJSON(w, map[string]any{})
	})
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		cfg := map[string]any{"decrypt": s.RT != nil && s.RT.HasKey(), "connected": s.Send != nil}
		if s.RT != nil {
			cfg["status"] = s.RT.Status()
		}
		writeJSON(w, cfg)
	})
	// Home Assistant connection (runtime).
	mux.HandleFunc("/api/ha_connect", func(w http.ResponseWriter, r *http.Request) {
		host := r.URL.Query().Get("host")
		if host == "" {
			host = r.URL.Query().Get("url") // back-compat
		}
		token := r.URL.Query().Get("token")
		if s.HA != nil && host != "" && token != "" {
			s.HA.Connect(names.WSURLFromHost(host), token)
			if s.SaveConfig != nil {
				s.SaveConfig(func(c *config.Config) { c.HAHost = host; c.HAToken = token })
			}
		}
		writeJSON(w, map[string]any{"ok": true, "url": names.WSURLFromHost(host)})
	})
	mux.HandleFunc("/api/ha_status", func(w http.ResponseWriter, r *http.Request) {
		if s.HA != nil {
			writeJSON(w, s.HA.Status())
			return
		}
		writeJSON(w, map[string]any{"connected": false})
	})
	// Philips Hue bridge: pair (link-button), status, and forget.
	mux.HandleFunc("/api/hue_pair", func(w http.ResponseWriter, r *http.Request) {
		host := r.URL.Query().Get("host")
		if host == "" || s.Hue == nil {
			writeJSON(w, map[string]any{"ok": false, "reason": "enter the bridge IP/host first"})
			return
		}
		key, err := names.HuePair(host)
		if err != nil {
			// Most common: the link button hasn't been pressed yet.
			writeJSON(w, map[string]any{"ok": false, "reason": err.Error()})
			return
		}
		s.Hue.Connect(host, key)
		if s.SaveConfig != nil {
			s.SaveConfig(func(c *config.Config) { c.HueHost = host; c.HueKey = key })
		}
		writeJSON(w, map[string]any{"ok": true, "paired": true})
	})
	mux.HandleFunc("/api/hue_status", func(w http.ResponseWriter, r *http.Request) {
		if s.Hue != nil {
			writeJSON(w, s.Hue.Status())
			return
		}
		writeJSON(w, map[string]any{"connected": false})
	})
	mux.HandleFunc("/api/hue_forget", func(w http.ResponseWriter, r *http.Request) {
		if s.Hue != nil {
			s.Hue.Connect("", "")
		}
		if s.SaveConfig != nil {
			s.SaveConfig(func(c *config.Config) { c.HueHost = ""; c.HueKey = "" })
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	// Save current key to the device's NVS (so it persists across power cycles).
	mux.HandleFunc("/api/save_key", func(w http.ResponseWriter, r *http.Request) {
		if s.RT != nil {
			s.cmd(proto.CmdSetKeyMsg(s.RT.Key())) // nil/!16 clears on-device
		}
		writeJSON(w, map[string]any{"saved": true})
	})
	// Raw command console: type=<int>&hex=<payload hex>. For advanced/debug use.
	mux.HandleFunc("/api/raw", func(w http.ResponseWriter, r *http.Request) {
		t, _ := strconv.Atoi(r.URL.Query().Get("type"))
		payload, err := hex.DecodeString(strings.NewReplacer(" ", "", ":", "").Replace(r.URL.Query().Get("hex")))
		if err != nil {
			writeJSON(w, map[string]any{"error": "bad hex"})
			return
		}
		s.cmd(proto.Encode(byte(t), payload))
		writeJSON(w, map[string]any{"sent": true, "type": t})
	})
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache") // always serve the current embedded UI
		w.Write(s.Web)
	})
	return mux
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer c.CloseNow()
	ch := s.Hub.sub()
	defer s.Hub.unsub(ch)
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-ch:
			if err := c.Write(ctx, websocket.MessageText, b); err != nil {
				return
			}
		}
	}
}

// nz returns an empty slice instead of nil so JSON encodes "[]" not "null".
func nz(v []map[string]any) []map[string]any {
	if v == nil {
		return []map[string]any{}
	}
	return v
}

var _ = context.Background
