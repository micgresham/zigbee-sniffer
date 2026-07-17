// Package api serves the REST + WebSocket API and the embedded web UI.
package api

import (
	"context"
	"encoding/binary"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"zbsniff/internal/config"
	"zbsniff/internal/decode"
	"zbsniff/internal/demo"
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

// statusHopDwell reads the device's current hop dwell (ms) from the latest status.
func statusHopDwell(rt *runtime.Runtime) int {
	if rt == nil {
		return 0
	}
	switch n := rt.Status()["hop_dwell_ms"].(type) {
	case int:
		return n
	case uint16:
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

	SendTo    func(port string, b []byte) bool // per-radio command send ("" = broadcast)
	Radios    *radios.Tracker                  // connected dongle identities
	Watch     *WatchSet                        // addresses flagged for join/rejoin activity
	OtaActive *atomic.Bool                     // true while an OTA transfer is in flight (see /api/ota) — tells the host's liveness watchdog not to force a reconnect and abort it
	OtaProg   *OtaProgress                     // last acknowledged OTA_STATUS "received" per target — see /api/ota's flow-control gate

	SaveConfig func(func(*config.Config)) // persist settings to the config file
	SetRole    func(radioID int, role string) // assign a radio's function
	Roles      func() map[int]string          // current radio-id → role map
	Reconnect  func()                          // force-reinit the serial link
	About      map[string]any                  // version/build metadata for the About tab
	Prefs      func() map[string]string        // current web-UI preferences (from config)
	Silence    func() int                      // current incident silence threshold (seconds)
	Channel    func() int                      // configured capture channel (for restore after a scan)
	HopDwell   func() int                      // configured hop dwell ms (0 = pinned)

	DemoActive         *atomic.Bool // true once demo mode (simulated data) is running
	NoHardwareDetected *atomic.Bool // true once the hardware search has gone a while with nothing found — the UI offers Retry/Enter Demo Mode rather than switching automatically
	EnterDemo          func()       // switches to demo mode (idempotent)
}

// incidentSilenceDefault mirrors the detector's default when unset.
const incidentSilenceDefault = 120

// injectCtr provides monotonic sequence + frame-counter values for injected ZDO
// frames (a fresh, high frame counter is accepted by devices as non-replayed).
var injectCtr atomic.Uint32

// writePcapTAP writes captured frames as a classic pcap with linktype
// LINKTYPE_IEEE802_15_4_TAP (283) — the modern encapsulation Wireshark uses,
// carrying per-frame RSSI/LQI/channel as TAP TLVs alongside the raw MPDU. Rows
// arrive newest-first (as stored); we emit them oldest-first.
func writePcapTAP(w io.Writer, rows []map[string]any) {
	gh := make([]byte, 24)
	binary.LittleEndian.PutUint32(gh[0:], 0xa1b2c3d4) // magic
	binary.LittleEndian.PutUint16(gh[4:], 2)          // version major
	binary.LittleEndian.PutUint16(gh[6:], 4)          // version minor
	binary.LittleEndian.PutUint32(gh[16:], 65535)     // snaplen
	binary.LittleEndian.PutUint32(gh[20:], 283)       // LINKTYPE_IEEE802_15_4_TAP
	w.Write(gh)
	toI := func(v any) int {
		switch n := v.(type) {
		case int64:
			return int(n)
		case int:
			return n
		case float64:
			return int(n)
		}
		return 0
	}
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		rawHex, _ := r["raw"].(string)
		mpdu, err := hex.DecodeString(rawHex)
		if err != nil || len(mpdu) == 0 {
			continue
		}
		// TAP TLVs (each: type u16, len u16, value, padded to 4 bytes).
		var tlvs []byte
		addTLV := func(typ uint16, val []byte) {
			h := make([]byte, 4)
			binary.LittleEndian.PutUint16(h[0:], typ)
			binary.LittleEndian.PutUint16(h[2:], uint16(len(val)))
			tlvs = append(append(tlvs, h...), val...)
			for len(tlvs)%4 != 0 {
				tlvs = append(tlvs, 0)
			}
		}
		addTLV(0, []byte{1}) // FCS type: 16-bit CRC present (MPDU includes FCS)
		chv := make([]byte, 3)
		binary.LittleEndian.PutUint16(chv[0:], uint16(toI(r["channel"]))) // channel + page 0
		addTLV(3, chv)
		rv := make([]byte, 4)
		binary.LittleEndian.PutUint32(rv, math.Float32bits(float32(toI(r["rssi"])))) // RSS dBm
		addTLV(1, rv)
		addTLV(10, []byte{byte(toI(r["lqi"]))}) // LQI

		tap := make([]byte, 4) // version 0, reserved 0, length
		binary.LittleEndian.PutUint16(tap[2:], uint16(4+len(tlvs)))
		pkt := append(append(tap, tlvs...), mpdu...)

		tsF, _ := r["ts"].(float64)
		rec := make([]byte, 16)
		binary.LittleEndian.PutUint32(rec[0:], uint32(int64(tsF)))
		binary.LittleEndian.PutUint32(rec[4:], uint32((tsF-math.Floor(tsF))*1e6))
		binary.LittleEndian.PutUint32(rec[8:], uint32(len(pkt)))
		binary.LittleEndian.PutUint32(rec[12:], uint32(len(pkt)))
		w.Write(rec)
		w.Write(pkt)
	}
}

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

// A relayed radio (a satellite reached over SPI, sharing the primary's port —
// see docs/multi-radio.md) can't be commanded with the plain per-port messages
// below: those always land on the port's own local radio. startOn/stopOn/
// setChannelOn pick the CMD_SAT_* wire commands instead when that's the case.
func (s *Server) relayed(port string, radioID int) bool {
	return s.Radios != nil && s.Radios.IsRelayed(port, radioID)
}

func (s *Server) startOn(port string, radioID int) {
	if s.relayed(port, radioID) {
		s.SendTo(port, proto.CmdSatStartMsg(byte(radioID)))
		return
	}
	s.sendFnTo(port, proto.CmdSetModeMsg(proto.ModeCapture), proto.CmdStartMsg())
}

func (s *Server) stopOn(port string, radioID int) {
	if s.relayed(port, radioID) {
		s.SendTo(port, proto.CmdSatStopMsg(byte(radioID)))
		return
	}
	s.sendFnTo(port, proto.CmdStopMsg())
}

// setChannelOn pins one specific radio to a channel — a relayed satellite
// moves independently of the primary's own channel (e.g. a multi-channel
// survey), while the primary's own local radio uses the plain global command.
func (s *Server) setChannelOn(port string, radioID int, ch byte) {
	if s.relayed(port, radioID) {
		s.SendTo(port, proto.CmdSatSetChannelMsg(byte(radioID), ch))
		return
	}
	s.sendFnTo(port, proto.CmdSetChannelMsg(ch))
}

// runSurvey hops every Zigbee channel, capturing briefly on each so foreign
// networks on other channels get recorded in the pans table. When active is set
// it also transmits a beacon request per channel, soliciting replies from even
// idle networks. It publishes per-channel progress on the WebSocket, then
// restores the original channel.
// intendedChannel is the configured capture channel to return to after a scan
// (NOT the momentary channel, which may be mid-hop). Falls back to last status.
func (s *Server) intendedChannel() int {
	if s.Channel != nil {
		if c := s.Channel(); c >= int(proto.ChannelMin) && c <= int(proto.ChannelMax) {
			return c
		}
	}
	return statusChannel(s.RT)
}
func (s *Server) intendedHop() int {
	if s.HopDwell != nil {
		return s.HopDwell()
	}
	return statusHopDwell(s.RT)
}

// restoreCapture returns a radio to the configured channel + hop state (pinned
// if hop is 0) and resumes capturing.
func (s *Server) restoreCapture(port string) int {
	frames := [][]byte{proto.CmdSetHopMsg(proto.AllChannelsMask(), uint16(s.intendedHop()))}
	ch := s.intendedChannel()
	if ch >= int(proto.ChannelMin) && ch <= int(proto.ChannelMax) {
		frames = append(frames, proto.CmdSetChannelMsg(byte(ch)))
	}
	frames = append(frames, proto.CmdSetModeMsg(proto.ModeCapture), proto.CmdStartMsg())
	s.sendFnTo(port, frames...)
	return ch
}

func (s *Server) runSurvey(port string, dwellMs int, active bool) {
	s.sendFnTo(port, proto.CmdSetHopMsg(proto.AllChannelsMask(), 0)) // stop hopping while we sweep
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
	restored := s.restoreCapture(port)
	s.Hub.Publish(map[string]any{"kind": "survey", "channel": restored, "active": active, "done": true})
}

// runMonitorSnapshot dwells the given radio on a foreign channel for a while
// (collecting that network's devices/names), then restores the capture channel.
// Used on a single radio when no spare is available to monitor continuously.
func (s *Server) runMonitorSnapshot(port string, ch, dwellMs int) {
	// Pin to the target channel (hop off) for a clean capture during the dwell.
	s.sendFnTo(port, proto.CmdSetHopMsg(proto.AllChannelsMask(), 0),
		proto.CmdSetChannelMsg(byte(ch)), proto.CmdSetModeMsg(proto.ModeCapture), proto.CmdStartMsg())
	s.Hub.Publish(map[string]any{"kind": "monitor", "channel": ch, "done": false})
	time.Sleep(time.Duration(dwellMs) * time.Millisecond)
	restored := s.restoreCapture(port) // back to the configured channel + hop state
	s.Hub.Publish(map[string]any{"kind": "monitor", "channel": restored, "done": true})
}

// hueChannel returns the paired Hue bridge's Zigbee channel, or 0.
func (s *Server) hueChannel() int {
	if s.Hue == nil {
		return 0
	}
	if ch, ok := s.Hue.Status()["channel"].(int); ok {
		return ch
	}
	return 0
}

// spareRadio picks a radio that can monitor a foreign channel without disrupting
// the primary capture: prefer an idle/unassigned radio or a satellite (radio-id
// != 0), never the active sniffer. Returns ok=false if only the primary exists.
func (s *Server) spareRadio() (port string, radioID int, ok bool) {
	if s.Radios == nil {
		return "", 0, false
	}
	roles := map[int]string{}
	if s.Roles != nil {
		roles = s.Roles()
	}
	list := s.Radios.List()
	for _, r := range list {
		role := roles[r.RadioID]
		if role == "sniffer" || role == "tester" || role == "hopper" {
			continue // busy on your network
		}
		if r.RadioID != 0 || role == "idle" || role == "" || strings.HasPrefix(role, "monitor") {
			if len(list) > 1 || r.RadioID != 0 { // need a genuine spare
				return r.Port, r.RadioID, true
			}
		}
	}
	return "", 0, false
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

// decodeDetail returns a layer-by-layer breakdown of one frame for the inspector.
func (s *Server) decodeDetail(mpdu []byte) map[string]any {
	var key []byte
	if s.RT != nil {
		key = s.RT.Key()
	}
	d := decode.DecodeFrame(mpdu, key)
	fmtA := func(a int64) string {
		if a < 0 {
			return "—"
		}
		if a > 0xFFFF {
			return fmt.Sprintf("0x%016x", uint64(a))
		}
		return fmt.Sprintf("0x%04x", uint16(a))
	}
	name := func(a int64) string {
		if a < 0 || a > 0xFFFF || s.Reg == nil {
			return ""
		}
		return s.Reg.NameFull(fmt.Sprintf("0x%04x", uint16(a)))
	}
	panS := func(p int64) string {
		if p < 0 {
			return "—"
		}
		return fmt.Sprintf("0x%04x", uint16(p))
	}
	out := map[string]any{"summary": d.Summary(), "hex": hex.EncodeToString(mpdu), "len": len(mpdu)}
	if m := d.MAC; m != nil {
		out["mac"] = map[string]any{
			"type": m.TypeName, "seq": m.Seq,
			"src": fmtA(m.SrcAddr), "src_name": name(m.SrcAddr), "dst": fmtA(m.DstAddr), "dst_name": name(m.DstAddr),
			"src_pan": panS(m.SrcPan), "dst_pan": panS(m.DstPan), "pan_compressed": m.PanComp,
		}
	}
	if n := d.NWK; n != nil {
		nwk := map[string]any{
			"src": fmt.Sprintf("0x%04x", uint16(n.Src)), "dst": fmt.Sprintf("0x%04x", uint16(n.Dst)),
			"radius": n.Radius, "seq": n.Seq, "secure": n.Secure, "decrypted": n.Decrypted,
		}
		if n.SrcExt != 0 {
			nwk["src_ieee"] = fmt.Sprintf("0x%016x", n.SrcExt)
		}
		if n.CommandID >= 0 {
			nwk["command"] = fmt.Sprintf("0x%02x", n.CommandID)
		}
		out["nwk"] = nwk
	}
	if a := d.APS; a != nil {
		out["aps"] = map[string]any{
			"type": a.FrameType, "cluster": clusterHex(a.Cluster), "profile": clusterHex(a.Profile),
			"src_ep": a.SrcEP, "dst_ep": a.DstEP,
		}
	}
	if z := d.ZCL; z != nil {
		out["zcl"] = map[string]any{"type": z.FrameType, "tsn": z.TSN, "command": z.CommandID, "summary": z.Summary()}
	}
	return out
}

func clusterHex(v int) string {
	if v < 0 {
		return "—"
	}
	return fmt.Sprintf("0x%04x", v)
}

// ieeeHex formats an extended (IEEE) address the way HA/zigpy conventionally
// display and log it: colon-separated big-endian bytes, e.g.
// "00:12:4b:00:11:22:33:44" — used to match this device's own log lines.
func ieeeHex(ext uint64) string {
	h := fmt.Sprintf("%016x", ext)
	parts := make([]string, 8)
	for i := 0; i < 8; i++ {
		parts[i] = h[i*2 : i*2+2]
	}
	return strings.Join(parts, ":")
}

// parseShortAddr parses "0x1234"/"1234" into a 16-bit short address.
func parseShortAddr(s string) (uint16, bool) {
	v, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "0x"), 16, 16)
	if err != nil {
		return 0, false
	}
	return uint16(v), true
}

// clusterNames maps common ZCL cluster ids to a friendly label for the UI.
var clusterNames = map[int]string{
	0x0000: "Basic", 0x0001: "Power Config", 0x0003: "Identify", 0x0004: "Groups",
	0x0005: "Scenes", 0x0006: "On/Off", 0x0008: "Level Control", 0x0019: "OTA Upgrade",
	0x0020: "Poll Control", 0x0102: "Window Covering", 0x0201: "Thermostat",
	0x0300: "Color Control", 0x0400: "Illuminance", 0x0402: "Temperature",
	0x0405: "Humidity", 0x0406: "Occupancy", 0x0500: "IAS Zone", 0x0702: "Metering",
	0x0b04: "Electrical Meas", 0x0b05: "Diagnostics", 0xfc00: "Manufacturer",
}

// traceroute returns the path from the coordinator to a device with per-hop LQI,
// preferring the coordinator's own neighbour table (ZHA) and falling back to the
// sniffer's observed links.
func (s *Server) traceroute(target uint16) ([]map[string]any, string, bool) {
	name := func(a uint16) string {
		n := ""
		if s.Reg != nil {
			n = s.Reg.NameFull(fmt.Sprintf("0x%04x", a))
		}
		if n == "" && a == 0 {
			n = "Coordinator"
		}
		return n
	}
	hop := func(a uint16, lqi int, has bool) map[string]any {
		h := map[string]any{"addr": fmt.Sprintf("0x%04x", a), "name": name(a)}
		if has {
			h["lqi"] = lqi
		}
		return h
	}
	// 1) ZHA neighbour parent-chain.
	if s.Reg != nil {
		nbrs := s.Reg.Neighbors()
		if len(nbrs) > 0 {
			parent := map[uint16]uint16{}
			plqi := map[uint16]int{}
			for dev, list := range nbrs {
				for _, n := range list {
					switch n.Relationship {
					case "Child":
						parent[n.NWK] = dev
						plqi[n.NWK] = n.LQI
					case "Parent":
						parent[dev] = n.NWK
						plqi[dev] = n.LQI
					}
				}
			}
			var chain []uint16
			seen := map[uint16]bool{}
			cur, complete := target, false
			for i := 0; i < 32; i++ {
				chain = append(chain, cur)
				if cur == 0 {
					complete = true
					break
				}
				if seen[cur] {
					break
				}
				seen[cur] = true
				p, ok := parent[cur]
				if !ok {
					break
				}
				cur = p
			}
			if complete {
				var hops []map[string]any
				for i := len(chain) - 1; i >= 0; i-- {
					a := chain[i]
					l, has := plqi[a]
					hops = append(hops, hop(a, l, has && a != 0))
				}
				return hops, "coordinator neighbour table (ZHA)", true
			}
		}
	}
	// 2) Fallback: BFS over the sniffer's observed links.
	edges, _ := s.DB.Routing()["edges"].([]map[string]any)
	adj := map[uint16][]uint16{}
	lqiOf := map[[2]uint16]int{}
	for _, e := range edges {
		src, ok1 := parseShortAddr(fmt.Sprint(e["src"]))
		dst, ok2 := parseShortAddr(fmt.Sprint(e["dst"]))
		if !ok1 || !ok2 {
			continue
		}
		adj[src] = append(adj[src], dst)
		adj[dst] = append(adj[dst], src)
		if l, ok := e["lqi"].(int64); ok {
			lqiOf[[2]uint16{src, dst}] = int(l)
			lqiOf[[2]uint16{dst, src}] = int(l)
		}
	}
	prev := map[uint16]uint16{0: 0}
	q := []uint16{0}
	found := false
	for len(q) > 0 && !found {
		n := q[0]
		q = q[1:]
		for _, m := range adj[n] {
			if _, ok := prev[m]; ok {
				continue
			}
			prev[m] = n
			if m == target {
				found = true
				break
			}
			q = append(q, m)
		}
	}
	if !found {
		return nil, "no path found — not enough observed links (connect Home Assistant, or capture longer)", false
	}
	var path []uint16
	for c := target; ; c = prev[c] {
		path = append([]uint16{c}, path...)
		if c == 0 {
			break
		}
	}
	var hops []map[string]any
	for i, a := range path {
		if i == 0 {
			hops = append(hops, hop(a, 0, false))
			continue
		}
		l, has := lqiOf[[2]uint16{path[i-1], a}]
		hops = append(hops, hop(a, l, has))
	}
	return hops, "observed links (sniffer)", true
}

// enrich fills name + network-reported signal (from HA) onto device rows. The
// sniffer's own rssi/lqi stay as-is; net_lqi/net_rssi are the coordinator's view.
// latestEdgePerSrc keeps one edge per src — preferring the physical MAC-layer
// hop (the true next-hop neighbor) over an NWK-layer logical pair, and only
// using recency to break ties within the same tier. This matters: the NWK
// src/dst pair is the packet's ultimate origin/destination and stays IDENTICAL
// across every hop of a multi-hop route, so it never reveals an intermediate
// router — and NWK frames are far more frequent than a specific MAC hop being
// directly overheard, so naively picking "whichever refreshed most recently"
// (see below) would almost always pick the NWK pair and permanently mask real
// routers even when the mesh is genuinely routing through them. Within the
// same tier, a device that has since re-routed through a new next-hop still
// correctly drops its stale edge in favor of the newer one. The underlying
// link history (counts, all past edges, both layers) is untouched in the DB —
// this only filters what the graph renders; export still sees the full
// history via DB.Routing() directly.
func latestEdgePerSrc(edges []map[string]any) []map[string]any {
	better := func(cur, e map[string]any) bool { // does e beat cur?
		curMac, eMac := cur["layer"] == "mac", e["layer"] == "mac"
		if curMac != eMac {
			return eMac
		}
		curTS, _ := cur["last_seen"].(float64)
		eTS, _ := e["last_seen"].(float64)
		return eTS > curTS
	}
	latest := map[string]map[string]any{} // src -> its preferred edge
	for _, e := range edges {
		src, _ := e["src"].(string)
		if cur, ok := latest[src]; !ok || better(cur, e) {
			latest[src] = e
		}
	}
	out := make([]map[string]any, 0, len(latest))
	for _, e := range latest {
		out = append(out, e)
	}
	return out
}

func (s *Server) enrich(rows []map[string]any) []map[string]any {
	if s.Reg == nil {
		return rows
	}
	ours := panHex(s.RT)
	for _, r := range rows {
		addr, ok := r["addr"].(string)
		if !ok {
			continue
		}
		// Flag devices on a different network so the UI can separate them. The
		// coordinator (0x0000) exists on every network — never flag it foreign.
		if pan, ok := r["pan"].(string); ok && pan != "" && ours != "" && pan != ours && addr != "0x0000" {
			r["foreign"] = true
		}
		if r["name"] == "" || r["name"] == nil {
			r["name"] = s.Reg.NameFull(addr) // friendly name, or a Hue-bridge name
		}
		// Manufacturer (OUI) fallback shown when no name is available.
		if mfg := s.Reg.Mfg(addr); mfg != "" {
			r["mfg"] = mfg
		}
		// Coordinator/Router/EndDevice as ZHA reported it — lets the graph use
		// HA's own role instead of guessing from observed edge degree.
		if dt := s.Reg.DeviceType(addr); dt != "" {
			r["device_type"] = dt
		}
		// HA's room/area assignment, for the graph's second label line.
		if area := s.Reg.Area(addr); area != "" {
			r["area"] = area
		}
		// HA's live reachability view — testing whether "offline in HA" tracks
		// with the unnamed-device cases (HA's own device page still shows a
		// name/manufacturer for offline devices normally, so this is mostly a
		// diagnostic to confirm or rule that out, not an expected root cause).
		if avail, known := s.Reg.Available(addr); known {
			r["ha_available"] = avail
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
			s.enrich(nodes) // marks foreign nodes; the client colours/toggles them
		}
		if edges, ok := rt["edges"].([]map[string]any); ok {
			rt["edges"] = latestEdgePerSrc(edges) // drop superseded routes from the live tree
		}
		writeJSON(w, rt)
	})
	// Device capabilities (endpoints/clusters/manufacturer/model/power) as the
	// coordinator (ZHA) discovered them — no on-air interrogation needed.
	mux.HandleFunc("/api/device_info", func(w http.ResponseWriter, r *http.Request) {
		short, ok := parseShortAddr(r.URL.Query().Get("addr"))
		if !ok || s.Reg == nil {
			writeJSON(w, map[string]any{"error": "bad addr"})
			return
		}
		info, has := s.Reg.Info(short)
		writeJSON(w, map[string]any{"addr": fmt.Sprintf("0x%04x", short), "have": has, "info": info,
			"clusters": clusterNames})
	})
	// Re-interview: force a fresh pull of device capabilities from the coordinator
	// (ZHA) — no on-air transmitting.
	mux.HandleFunc("/api/reinterview", func(w http.ResponseWriter, r *http.Request) {
		if s.HA != nil {
			s.HA.Refresh()
		}
		writeJSON(w, map[string]any{"ok": s.HA != nil})
	})
	// Traceroute: the path from the coordinator to a device, with per-hop LQI.
	mux.HandleFunc("/api/traceroute", func(w http.ResponseWriter, r *http.Request) {
		short, ok := parseShortAddr(r.URL.Query().Get("addr"))
		if !ok {
			writeJSON(w, map[string]any{"error": "bad addr"})
			return
		}
		hops, source, complete := s.traceroute(short)
		writeJSON(w, map[string]any{"target": fmt.Sprintf("0x%04x", short),
			"hops": hops, "source": source, "complete": complete})
	})
	// Active ZDO interrogation. Builds an NWK-secured ZDO request (bindings /
	// endpoints / node descriptor) and TRANSMITS it on the network. The device's
	// reply is captured passively and appears in its recent messages.
	// ⚠ This transmits — gate behind an explicit user confirmation.
	mux.HandleFunc("/api/interrogate", func(w http.ResponseWriter, r *http.Request) {
		target, ok := parseShortAddr(r.URL.Query().Get("addr"))
		if !ok {
			writeJSON(w, map[string]any{"ok": false, "reason": "bad addr"})
			return
		}
		var key []byte
		if s.RT != nil {
			key = s.RT.Key()
		}
		if len(key) != 16 {
			writeJSON(w, map[string]any{"ok": false, "reason": "no network key set — can't build a secured frame"})
			return
		}
		var cluster uint16
		var args []byte
		switch r.URL.Query().Get("what") {
		case "endpoints":
			cluster, args = decode.ZDOActiveEPReq, []byte{byte(target), byte(target >> 8)}
		case "node":
			cluster, args = decode.ZDONodeDescReq, []byte{byte(target), byte(target >> 8)}
		default: // bindings
			cluster, args = decode.ZDOMgmtBindReq, []byte{0x00} // StartIndex 0
		}
		seq := byte(injectCtr.Add(1))
		frame := decode.BuildZDORequest(decode.ZDORequest{
			TargetShort: target, MacDst: target, SrcShort: 0xfffe,
			SrcExt: 0x00124bfffffffe01, DstPan: uint16(panOf(s.RT)),
			Cluster: cluster, ZDPArgs: args,
			FrameCounter: 0x30000000 + injectCtr.Load(),
			MacSeq:       seq, NwkSeq: seq, ApsSeq: seq, Tsn: seq, Key: key,
		})
		if frame == nil {
			writeJSON(w, map[string]any{"ok": false, "reason": "failed to build frame"})
			return
		}
		port, _, okA, _, reason := s.allocate("probe")
		if !okA {
			writeJSON(w, map[string]any{"ok": false, "reason": reason})
			return
		}
		s.sendFnTo(port, proto.CmdTxRawMsg(frame))
		writeJSON(w, map[string]any{"ok": true, "target": fmt.Sprintf("0x%04x", target),
			"bytes": len(frame)})
	})
	mux.HandleFunc("/api/incidents", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, nz(s.DB.Incidents(200)))
	})
	// Device analysis: every record the store holds about one device (full
	// incident history, route failures, active-probe results, RSSI/LQI trend)
	// plus rule-based findings on why it might be going unresponsive — and,
	// if HA is connected, its own ZHA/zigpy log entries mentioning this
	// device: the layer a passive sniffer can't see (a command that never
	// made it onto the air, a timeout, an exception inside the integration).
	mux.HandleFunc("/api/device_analysis", func(w http.ResponseWriter, r *http.Request) {
		short, ok := parseShortAddr(r.URL.Query().Get("addr"))
		if !ok {
			writeJSON(w, map[string]any{"error": "bad addr"})
			return
		}
		analysis := s.DB.DeviceAnalysis(int(short))
		addrHex := fmt.Sprintf("0x%04x", short)
		needles := []string{addrHex}
		if s.Reg != nil {
			if name := s.Reg.NameFull(addrHex); name != "" {
				needles = append(needles, name)
			}
			if ext, ok := s.Reg.ExtOf(short); ok {
				needles = append(needles, ieeeHex(ext))
			}
		}
		logs := nz(s.DB.HALogsFor(needles, 50))
		analysis["ha_logs"] = logs
		// ERROR/WARNING entries are exactly the "why" a device analysis is
		// for — fold them into findings alongside the RF-derived ones.
		findings, _ := analysis["findings"].([]map[string]any)
		for _, l := range logs {
			lvl, _ := l["level"].(string)
			if lvl != "ERROR" && lvl != "WARNING" {
				continue
			}
			sev := "warning"
			if lvl == "ERROR" {
				sev = "error"
			}
			findings = append(findings, map[string]any{
				"severity": sev, "category": "HA/zigpy log",
				"message": fmt.Sprintf("%v (logged %vx by %v) — Home Assistant/zigpy reported this about the device directly; the sniffer's RF view alone wouldn't show it",
					l["message"], l["count"], l["logger"]),
			})
		}
		analysis["findings"] = findings
		writeJSON(w, analysis)
	})
	// Diagnostics runs a dozen-plus analysis queries behind the single DB
	// mutex; every open tab polls it every 3 s. A short cache bounds that
	// cost regardless of tab count — findings don't change second-to-second.
	var dgMu sync.Mutex
	var dgAt time.Time
	var dgCached []map[string]any
	mux.HandleFunc("/api/diagnostics", func(w http.ResponseWriter, r *http.Request) {
		dgMu.Lock()
		if time.Since(dgAt) > 10*time.Second {
			dgCached = nz(s.DB.Diagnostics(panOf(s.RT)))
			dgAt = time.Now()
		}
		out := dgCached
		dgMu.Unlock()
		writeJSON(w, out)
	})
	mux.HandleFunc("/api/stats", func(w http.ResponseWriter, r *http.Request) {
		if s.Stats != nil {
			writeJSON(w, s.Stats.Snapshot())
			return
		}
		writeJSON(w, map[string]any{})
	})
	// Demo mode: the dashboard polls this to decide whether to show the
	// "no hardware found" banner (Retry / Enter Demo Mode) or the persistent
	// "demo mode active" banner — never switches automatically either way.
	mux.HandleFunc("/api/demo/status", func(w http.ResponseWriter, r *http.Request) {
		demoActive := s.DemoActive != nil && s.DemoActive.Load()
		noHW := s.NoHardwareDetected != nil && s.NoHardwareDetected.Load()
		writeJSON(w, map[string]any{"demo": demoActive, "no_hardware_detected": noHW && !demoActive})
	})
	mux.HandleFunc("/api/demo/enable", func(w http.ResponseWriter, r *http.Request) {
		if s.EnterDemo != nil {
			s.EnterDemo()
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	// "Retry" doesn't need to do anything to the (already-continuous) hardware
	// search — it just clears the banner's flag so the UI stops offering demo
	// mode until the search has gone quiet for a while again.
	mux.HandleFunc("/api/demo/retry", func(w http.ResponseWriter, r *http.Request) {
		if s.NoHardwareDetected != nil {
			s.NoHardwareDetected.Store(false)
		}
		writeJSON(w, map[string]any{"ok": true})
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
	// Full layer-by-layer decode of a single frame (by raw hex), for the inspector.
	mux.HandleFunc("/api/decode", func(w http.ResponseWriter, r *http.Request) {
		raw, err := hex.DecodeString(strings.TrimSpace(r.URL.Query().Get("hex")))
		if err != nil || len(raw) == 0 {
			writeJSON(w, map[string]any{"error": "bad hex"})
			return
		}
		writeJSON(w, s.decodeDetail(raw))
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
		// Refuse a second transfer while one's already in flight: every POST
		// here spawns an independent goroutine that runs to completion
		// regardless of later requests, so without this guard, overlapping
		// attempts would interleave OTA_BEGIN/DATA/END against the same
		// target — each fresh OTA_BEGIN restarts the device's own OTA state
		// machine, so the earlier attempt never has a chance to finish and
		// neither does the new one; it looks like a hung transfer forever.
		if s.OtaActive != nil && !s.OtaActive.CompareAndSwap(false, true) {
			writeJSON(w, map[string]any{"ok": false, "reason": "an OTA transfer is already in progress — wait for it to finish"})
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
		log.Printf("OTA: starting transfer to target %d (%d bytes, crc %08x) — pausing all radios for the duration", target, len(data), crc)
		go func() {
			if s.OtaActive != nil {
				defer time.AfterFunc(5*time.Second, func() { s.OtaActive.Store(false) })
				// The 5s grace period after OTA_END covers the device processing
				// the end-of-image check and rebooting — also a legitimate silence
				// the watchdog shouldn't react to.
			}
			// Every radio's capture/spectrum/etc competes with the OTA relay for
			// the same USB/SPI links (this is exactly what was silently corrupting
			// satellite OTA replies before the SPI relay fix) — pause all of them
			// for the transfer, and resume whatever each was doing afterward.
			var savedRoles map[int]string
			if s.Roles != nil {
				savedRoles = s.Roles()
			}
			if s.Radios != nil {
				for _, rd := range s.Radios.List() {
					s.stopOn(rd.Port, rd.RadioID)
				}
			}
			resume := func() {
				if s.SetRole == nil {
					return
				}
				for radioID, role := range savedRoles {
					if role != "" {
						s.SetRole(radioID, role)
					}
				}
			}
			if s.OtaProg != nil {
				s.OtaProg.Reset(target) // a stale count from an earlier attempt would let the gate below race ahead
			}
			beginOK := true
			send(proto.CmdOtaBeginMsg(byte(target), uint32(len(data)), crc))
			if target != 0 && s.OtaProg != nil {
				// CMD_OTA_BEGIN is just as much a single, un-redundant message
				// as END — if it's lost, the satellite silently stays in
				// OTA_IDLE and every subsequent chunk fails its state check
				// forever (observed: the whole transfer stalls at offset 0),
				// with no error anywhere since ota_write()'s failure is
				// swallowed and status still reports normally otherwise.
				beginOK = false
				for attempt := 0; attempt < 4 && !beginOK; attempt++ {
					if attempt > 0 {
						send(proto.CmdOtaBeginMsg(byte(target), uint32(len(data)), crc))
					}
					deadline := time.Now().Add(2 * time.Second)
					for time.Now().Before(deadline) {
						if s.OtaProg.State(target) == proto.OtaReceiving {
							beginOK = true
							break
						}
						time.Sleep(20 * time.Millisecond)
					}
				}
				if !beginOK {
					log.Printf("OTA: target %d never confirmed OTA_BEGIN — aborting before sending any data", target)
				}
			} else {
				time.Sleep(400 * time.Millisecond) // let esp_ota_begin() erase/prepare (target 0: no confirmation channel, keep the old fixed wait)
			}
			// Satellite chunks are relayed over SPI (256-byte transactions), so
			// keep them small enough to fit one transaction incl. framing.
			chunk := 512
			if target != 0 {
				chunk = 200
			}
			aborted := !beginOK
			for off := 0; off < len(data) && !aborted; off += chunk {
				end := off + chunk
				if end > len(data) {
					end = len(data)
				}
				// esp_ota_write() has no offset parameter — it just appends
				// sequentially wherever its internal write pointer is. So a
				// single silently-dropped chunk (the satellite's OTA queue is
				// small and a synchronous flash write can't always keep up
				// with a fixed send pace) doesn't just leave a gap: every
				// later chunk lands one position early forever after,
				// corrupting the rest of the image with zero visible error
				// until the final esp_ota_end() validation fails. So don't
				// just cap how far ahead we can get — actually confirm the
				// satellite's own acknowledged byte count reaches THIS
				// chunk's end before advancing, resending it if not.
				if target != 0 && s.OtaProg != nil {
					want := uint32(end)
					confirmed := false
					for attempt := 0; attempt < 8 && !confirmed; attempt++ {
						if attempt > 0 {
							// Visibility only: on the satellite's currently-running
							// (pre-dedup) firmware, this resend is exactly what
							// double-writes and corrupts the image — logging it
							// lets a failure be correlated with "a retry happened
							// here" instead of looking like unexplained corruption.
							log.Printf("OTA: target %d chunk at offset %d not yet confirmed — resending (attempt %d/8)", target, off, attempt+1)
						}
						send(proto.CmdOtaDataMsg(byte(target), uint32(off), data[off:end]))
						// Tried extending this to 22s on the theory that retries
						// were firing on a periodic near-miss (replies genuinely
						// in flight, just past a too-tight deadline) — but retries
						// kept recurring at roughly THAT new deadline too, meaning
						// most of these are replies that never arrive at all, not
						// slow ones. Waiting longer per attempt just wastes time
						// on a genuinely lost reply; a shorter deadline with many
						// more attempts detects the loss and gets a fresh
						// transmission (and thus a fresh chance to be received
						// cleanly) faster.
						deadline := time.Now().Add(5 * time.Second)
						for time.Now().Before(deadline) {
							if s.OtaProg.Get(target) >= want {
								confirmed = true
								break
							}
							time.Sleep(10 * time.Millisecond)
						}
					}
					if !confirmed {
						log.Printf("OTA: target %d never acknowledged the chunk at offset %d after retries — aborting rather than send a corrupt image", target, off)
						send(proto.CmdOtaAbortMsg(byte(target)))
						aborted = true
					}
					continue // chunk (re)sent above, along with its confirmation wait
				}
				send(proto.CmdOtaDataMsg(byte(target), uint32(off), data[off:end]))
				time.Sleep(8 * time.Millisecond) // pace flash writes
			}
			if aborted {
				if s.OtaActive != nil {
					s.OtaActive.Store(false)
				}
				resume()
				return
			}
			send(proto.CmdOtaEndMsg(byte(target)))
			// Unlike DATA (1000+ chunks, so losing one occasionally doesn't
			// matter), OTA_END is a single one-shot message — if it's lost in
			// transit (a real, observed SPI hiccup, not just a software race),
			// the device silently never reboots despite every byte of the
			// image having arrived intact, with nothing to show for it. Confirm
			// via a state transition and resend if it doesn't land.
			if target != 0 && s.OtaProg != nil {
				var baselineUptime int
				var haveBaseline bool
				if s.Radios != nil {
					baselineUptime, haveBaseline = s.Radios.UptimeOf(target)
				}
				confirmed := false
				for attempt := 0; attempt < 5 && !confirmed; attempt++ {
					deadline := time.Now().Add(2 * time.Second)
					for time.Now().Before(deadline) {
						if st := s.OtaProg.State(target); st == proto.OtaOK || st == proto.OtaError {
							confirmed = true
							break
						}
						time.Sleep(50 * time.Millisecond)
					}
					if !confirmed {
						log.Printf("OTA: no confirmation of OTA_END for target %d yet — resending (attempt %d/5)", target, attempt+2)
						send(proto.CmdOtaEndMsg(byte(target)))
					}
				}
				if !confirmed {
					// The OTA_END reply itself can be lost even when the update
					// actually succeeded and the device rebooted — that's exactly
					// what happened in testing (every byte confirmed, image
					// verified, reboot happened, but this one final ack didn't
					// make it back). Rather than report a false failure, fall
					// back to an independent signal: a reboot resets the
					// device's own uptime counter, which its regular status
					// heartbeat reports whether or not OTA_END's reply landed.
					rebooted := false
					if haveBaseline && s.Radios != nil {
						deadline := time.Now().Add(6 * time.Second)
						for time.Now().Before(deadline) {
							if u, ok := s.Radios.UptimeOf(target); ok && u < baselineUptime {
								rebooted = true
								break
							}
							time.Sleep(200 * time.Millisecond)
						}
					}
					if rebooted {
						log.Printf("OTA: target %d rebooted (uptime reset) — completion confirmed despite a lost OTA_END reply", target)
					} else {
						log.Printf("OTA: target %d never confirmed completion after retries — it may not have rebooted", target)
					}
				}
			}
			// Give the target a moment to validate/reboot before resuming traffic
			// on the rest of the link — mirrors the watchdog's own grace window.
			time.Sleep(2 * time.Second)
			log.Printf("OTA: transfer to target %d complete — resuming paused radios", target)
			resume()
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
		if r.URL.Query().Get("format") == "pcap" && (what == "frames" || what == "packets") {
			w.Header().Set("Content-Type", "application/vnd.tcpdump.pcap")
			w.Header().Set("Content-Disposition", "attachment; filename=zbsniff-"+ts+".pcap")
			writePcapTAP(w, rows)
			return
		}
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
	// Zero every radio's frame/probe counters for a fresh count — doesn't
	// touch identity, role, channel, or captured history in the DB.
	mux.HandleFunc("/api/reset_stats", func(w http.ResponseWriter, r *http.Request) {
		if s.Radios != nil {
			s.Radios.Reset()
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	// Purge all captured data (frames, devices, routing, incidents, spectrum,
	// probe history, schedules) and learned identity data (names/areas/roles
	// cache) — a clean slate for topology/device data, keeping settings
	// (HA/Hue credentials, network key, saved ports) untouched. Takes effect
	// immediately, no restart needed.
	mux.HandleFunc("/api/purge_data", func(w http.ResponseWriter, r *http.Request) {
		if s.DB != nil {
			if err := s.DB.Reset(); err != nil {
				writeJSON(w, map[string]any{"ok": false, "reason": err.Error()})
				return
			}
		}
		if s.Reg != nil {
			s.Reg.Reset()
		}
		if s.Radios != nil {
			s.Radios.Reset()
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	// Full factory reset: everything /api/purge_data does, PLUS resets
	// zbsniff.yaml (HA/Hue credentials, network key, saved ports/channel,
	// radio roles, UI prefs) to defaults. The config-file part only takes
	// effect on the NEXT process restart — this handler can't safely tear
	// down and rebuild the live HA/Hue/serial connections in place, so the
	// current session keeps running on the old settings until restarted.
	mux.HandleFunc("/api/factory_reset", func(w http.ResponseWriter, r *http.Request) {
		if s.DB != nil {
			if err := s.DB.Reset(); err != nil {
				writeJSON(w, map[string]any{"ok": false, "reason": err.Error()})
				return
			}
		}
		if s.Reg != nil {
			s.Reg.Reset()
		}
		if s.Radios != nil {
			s.Radios.Reset()
		}
		if s.SaveConfig != nil {
			s.SaveConfig(func(c *config.Config) {
				c.HAHost, c.HAToken = "", ""
				c.HueHost, c.HueKey = "", ""
				c.Key = ""
				c.ZHABackup = ""
				c.RadioRoles = ""
				c.HopDwellMs = 0
				c.Mode = 0
				c.Channel = 0
				c.IncidentSilenceS = 0
				c.Ports = nil
				c.UIPrefs = map[string]string{}
			})
		}
		writeJSON(w, map[string]any{"ok": true, "restart_required": true})
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
	// Manually pin one radio to a channel, independent of the others — e.g. run
	// a satellite on a different channel than the primary for multi-channel
	// capture. Relayed (SPI satellite) radios get the CMD_SAT_* wire commands;
	// the primary's own local radio uses the plain global one.
	mux.HandleFunc("/api/radio_channel", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.URL.Query().Get("radio"))
		ch, chErr := strconv.Atoi(r.URL.Query().Get("ch"))
		if err != nil || chErr != nil || ch < int(proto.ChannelMin) || ch > int(proto.ChannelMax) {
			http.Error(w, "need a valid radio id and channel (11-26)", 400)
			return
		}
		if s.SendTo == nil || s.Radios == nil {
			writeJSON(w, map[string]any{"ok": false, "reason": "no radios tracked yet"})
			return
		}
		ports := s.Radios.PortsFor(id)
		if len(ports) == 0 {
			writeJSON(w, map[string]any{"ok": false, "reason": "unknown radio id"})
			return
		}
		s.setChannelOn(ports[0], id, byte(ch))
		// Radio 0 (the primary's own local radio, see radios.resolve) is the one
		// case where persistence isn't handled on-device alone from the host's
		// point of view: a satellite remembers its own channel in its own NVS
		// regardless of what the host does, but radio 0's restore-on-startup
		// (see the legacy resend sequence in main.go) reads this saved config,
		// so it needs to be kept in sync here too — otherwise a per-radio change
		// to radio 0 would silently revert on the next zbsniff restart.
		if id == 0 && s.SaveConfig != nil {
			s.SaveConfig(func(c *config.Config) { c.Channel = ch })
		}
		writeJSON(w, map[string]any{"ok": true, "radio": id, "channel": ch})
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
	// Watch: flag an address in the device drawer so ingest() pushes a "watch"
	// WS event (with a join_related flag) the moment it transmits again —
	// meant for a "ghost" device that's gone quiet, without babysitting the
	// Live Frames table for it.
	mux.HandleFunc("/api/watch_add", func(w http.ResponseWriter, r *http.Request) {
		addr, ok := parseAddr(r.URL.Query().Get("addr"))
		if !ok || s.Watch == nil {
			http.Error(w, "bad addr", 400)
			return
		}
		s.Watch.Add(fmt.Sprintf("0x%04x", uint16(addr)))
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/watch_remove", func(w http.ResponseWriter, r *http.Request) {
		addr, ok := parseAddr(r.URL.Query().Get("addr"))
		if !ok || s.Watch == nil {
			http.Error(w, "bad addr", 400)
			return
		}
		s.Watch.Remove(fmt.Sprintf("0x%04x", uint16(addr)))
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/watch_list", func(w http.ResponseWriter, r *http.Request) {
		if s.Watch == nil {
			writeJSON(w, map[string]any{"addrs": []string{}})
			return
		}
		writeJSON(w, map[string]any{"addrs": s.Watch.List()})
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
	// Per-channel airtime: frames/min by PAN plus per-PAN RSSI histograms. A
	// loud neighbouring network shows up here as its frame rate and RSSI
	// distribution instead of needing "unplug things until it works".
	mux.HandleFunc("/api/airtime", func(w http.ResponseWriter, r *http.Request) {
		win := 3600
		if v, err := strconv.Atoi(r.URL.Query().Get("window")); err == nil && v >= 900 && v <= 172800 {
			win = v
		}
		out := s.DB.Airtime(win)
		ours := panHex(s.RT)
		hueCh := s.hueChannel()
		if pans, ok := out["pans"].([]map[string]any); ok {
			for _, p := range pans {
				pan, _ := p["pan"].(string)
				p["ours"] = pan == ours
				// Same bridge-fact labeling as /api/networks: a paired Hue
				// bridge's channel identifies a foreign PAN there as Hue.
				if pan != ours && hueCh > 0 && p["label"] == "" {
					if chs, ok := p["channels"].([]int); ok && len(chs) > 0 && chs[0] == hueCh {
						p["label"] = "Philips Hue"
					}
				}
			}
		}
		writeJSON(w, out)
	})
	// Zigbee networks (PAN ids) seen on-air, and which one is ours.
	mux.HandleFunc("/api/networks", func(w http.ResponseWriter, r *http.Request) {
		nets := nz(s.DB.Networks())
		ours := panHex(s.RT)
		// Authoritative network identity: a paired Hue bridge reports its own
		// Zigbee channel, so a FOREIGN network on that channel IS the Hue network —
		// label it "Philips Hue" (a bridge fact, overriding any per-device OUI).
		hueCh := s.hueChannel()
		for _, n := range nets {
			pan, _ := n["pan"].(string)
			ch, _ := n["channel"].(int64)
			if pan != ours && hueCh > 0 && int(ch) == hueCh {
				n["label"] = "Philips Hue"
			}
		}
		writeJSON(w, map[string]any{"networks": nets, "ours": ours})
	})
	// Explicitly (re)designate which PAN is "home". Needed because the only
	// automatic source (a ZHA backup file) is a point-in-time snapshot — if
	// the network's real PAN ever changes (e.g. Zigbee's own PAN-conflict
	// resolution), every real device silently starts reading as "foreign"
	// with nothing louder than a broken routing tree to notice by. No on-air
	// transmitting — this only changes which PAN this host treats as home.
	mux.HandleFunc("/api/set_home_network", func(w http.ResponseWriter, r *http.Request) {
		pan, ok := parseShortAddr(r.URL.Query().Get("pan"))
		if !ok || pan == 0 {
			writeJSON(w, map[string]any{"ok": false, "reason": "bad pan"})
			return
		}
		if s.RT != nil {
			s.RT.SetPan(pan)
		}
		if s.SaveConfig != nil {
			s.SaveConfig(func(c *config.Config) { c.PanID = pan })
		}
		writeJSON(w, map[string]any{"ok": true, "pan": fmt.Sprintf("0x%04x", pan)})
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
	// Monitor a foreign network. Target channel = ?channel=, else the paired Hue
	// bridge's channel (prioritised), else the busiest foreign network. If a spare
	// (satellite) radio is available it's dedicated to that channel continuously;
	// otherwise the primary takes a timed snapshot there and returns.
	mux.HandleFunc("/api/monitor", func(w http.ResponseWriter, r *http.Request) {
		ch, _ := strconv.Atoi(r.URL.Query().Get("channel"))
		source := "requested"
		if ch == 0 {
			if hc := s.hueChannel(); hc > 0 {
				ch, source = hc, "Hue bridge"
			}
		}
		if ch == 0 { // busiest foreign network
			ours := panHex(s.RT)
			for _, n := range s.DB.Networks() {
				pan, _ := n["pan"].(string)
				c, _ := n["channel"].(int64)
				if pan != ours && c >= int64(proto.ChannelMin) && c <= int64(proto.ChannelMax) {
					ch, source = int(c), "foreign network "+pan
					break
				}
			}
		}
		if ch < int(proto.ChannelMin) || ch > int(proto.ChannelMax) {
			writeJSON(w, map[string]any{"ok": false, "reason": "no Hue bridge or foreign network to monitor yet — run a survey first"})
			return
		}
		if port, id, ok := s.spareRadio(); ok && s.SetRole != nil {
			_ = port
			s.SetRole(id, fmt.Sprintf("monitor:%d", ch))
			writeJSON(w, map[string]any{"ok": true, "mode": "radio", "radio": id, "channel": ch, "source": source})
			return
		}
		port, _, ok, _, reason := s.allocate("capture")
		if !ok {
			writeJSON(w, map[string]any{"ok": false, "reason": reason})
			return
		}
		dwell := 20000
		if d, err := strconv.Atoi(r.URL.Query().Get("dwell")); err == nil && d >= 2000 {
			dwell = d
		}
		go s.runMonitorSnapshot(port, ch, dwell)
		writeJSON(w, map[string]any{"ok": true, "mode": "snapshot", "channel": ch, "source": source, "dwell_ms": dwell})
	})
	mux.HandleFunc("/api/set_channel", func(w http.ResponseWriter, r *http.Request) {
		ch, _ := strconv.Atoi(r.URL.Query().Get("ch"))
		if ch >= proto.ChannelMin && ch <= proto.ChannelMax && s.Send != nil {
			s.Send(proto.CmdSetChannelMsg(byte(ch)))
			s.Send(proto.CmdSetModeMsg(proto.ModeCapture))
			s.Send(proto.CmdStartMsg())
			// This is the GLOBAL "everyone" channel control, unlike /api/radio_channel
			// (one radio) — explicitly push it to every relayed satellite too, since
			// CMD_SET_CHANNEL alone only ever reaches a port's own local radio.
			if s.Radios != nil && s.SendTo != nil {
				for _, rd := range s.Radios.List() {
					if rd.Relayed {
						s.SendTo(rd.Port, proto.CmdSatSetChannelMsg(byte(rd.RadioID), byte(ch)))
					}
				}
			}
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
		port, radioID, ok, _, reason := s.allocate("spectrum")
		if !ok {
			writeJSON(w, map[string]any{"ok": false, "reason": reason})
			return
		}
		if s.DemoActive != nil && s.DemoActive.Load() {
			// Same allocation as real hardware: whichever radio is chosen
			// briefly stops sniffing for the sweep's duration (a single
			// radio can't do both at once), not just paint fake ED data on
			// top of traffic that's still "running."
			demo.TriggerScan(radioID, dwell)
		} else {
			s.sendFnTo(port, proto.CmdEdScanMsg(proto.AllChannelsMask(), uint16(dwell)))
		}
		writeJSON(w, map[string]any{"ok": true, "scanning": true, "dwell": dwell})
	})
	mux.HandleFunc("/api/start", func(w http.ResponseWriter, r *http.Request) {
		port, radioID, ok, _, reason := s.allocate("capture")
		if !ok {
			writeJSON(w, map[string]any{"ok": false, "reason": reason})
			return
		}
		s.startOn(port, radioID)
		if s.Radios != nil {
			s.Radios.SetStopped(port, radioID, false)
		}
		writeJSON(w, map[string]any{"ok": true, "capturing": true})
	})
	mux.HandleFunc("/api/stop", func(w http.ResponseWriter, r *http.Request) {
		s.cmd(proto.CmdStopMsg()) // every port's own local radio
		if s.Radios != nil {
			for _, rd := range s.Radios.List() { // + every relayed satellite
				if rd.Relayed && s.SendTo != nil {
					s.SendTo(rd.Port, proto.CmdSatStopMsg(byte(rd.RadioID)))
				}
			}
			s.Radios.SetStopped("", -1, true) // all radios
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
		port, radioID := "", -1
		if fn != "" {
			p, rid, ok, _, reason := s.allocate(fn)
			if !ok {
				writeJSON(w, map[string]any{"ok": false, "reason": reason})
				return
			}
			port, radioID = p, rid
		}
		if s.DemoActive != nil && s.DemoActive.Load() {
			// Same allocation as real hardware — whichever radio is chosen
			// for "spectrum" actually stops sniffing while the sweep runs
			// (see demo.StartContinuousScan), instead of running both at once.
			switch m {
			case proto.ModeEdSweep:
				demo.StartContinuousScan(radioID, 20)
			case proto.ModeCapture, proto.ModeCapturePlusEd:
				demo.StopContinuousScan()
			}
			writeJSON(w, map[string]any{"ok": true, "mode": m})
			return
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
		if s.DemoActive != nil && s.DemoActive.Load() {
			writeJSON(w, map[string]any{"connected": true, "devices": len(demo.HomeDevices) + 1})
			return
		}
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
		if s.DemoActive != nil && s.DemoActive.Load() {
			writeJSON(w, map[string]any{"connected": true, "devices": len(demo.HueDevices) - 1, "channel": demo.HueChan, "host": "demo-bridge"})
			return
		}
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
