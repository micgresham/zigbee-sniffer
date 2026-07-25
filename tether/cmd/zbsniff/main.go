// Command zbsniff is the tethered (USB) host: it reads framed captures from one
// or more ESP32-C6 sniffer dongles, decodes (+ optionally decrypts) them, stores
// them in SQLite, and serves a web UI + REST/WebSocket API. Single static binary.
package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"zbsniff/internal/api"
	"zbsniff/internal/config"
	"zbsniff/internal/decode"
	"zbsniff/internal/demo"
	"zbsniff/internal/logbuf"
	"zbsniff/internal/names"
	"zbsniff/internal/proto"
	"zbsniff/internal/radios"
	"zbsniff/internal/runtime"
	"zbsniff/internal/sched"
	"zbsniff/internal/serialio"
	"zbsniff/internal/stats"
	"zbsniff/internal/store"
	"zbsniff/internal/webui"
)

// hostVersion is the zbsniff host app version (bump on release; can also be
// overridden at build time with -ldflags "-X main.hostVersion=…").
var hostVersion = "1.1.1"

const author = "M. Gresham"

// aboutInfo gathers version/build metadata for the About tab. It works without
// ldflags: git details come from the embedded build info when available, and the
// build date falls back to the binary's own modification time.
func aboutInfo() map[string]any {
	m := map[string]any{
		"app":       "Zigbee Sniffer — tethered host (zbsniff)",
		"version":   hostVersion,
		"author":    author,
		"go":        goruntime.Version(),
		"platform":  goruntime.GOOS + "/" + goruntime.GOARCH,
		"proto_ver": proto.Ver,
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if len(s.Value) >= 7 {
					m["commit"] = s.Value[:7]
				}
			case "vcs.time":
				m["build_date"] = s.Value
			case "vcs.modified":
				if s.Value == "true" {
					m["dirty"] = true
				}
			}
		}
	}
	if _, ok := m["build_date"]; !ok {
		if exe, err := os.Executable(); err == nil {
			if fi, err := os.Stat(exe); err == nil {
				m["build_date"] = fi.ModTime().Format("2006-01-02 15:04:05 MST")
			}
		}
	}
	return m
}

type portList []string

func (p *portList) String() string { return strings.Join(*p, ",") }
func (p *portList) Set(v string) error {
	*p = append(*p, v)
	return nil
}

func main() {
	var ports portList
	flag.Var(&ports, "port", "serial port (repeatable for multiple radios)")
	baud := flag.Int("baud", 921600, "serial baud")
	channel := flag.Int("channel", 0, "set capture channel 11-26 on start")
	dbPath := flag.String("db", "zbsniff.sqlite", "SQLite database path")
	keyHex := flag.String("key", "", "Zigbee network key (32 hex chars) for decryption")
	httpPort := flag.Int("http-port", 8080, "HTTP port")
	zhaBackup := flag.String("zha-backup", "", "ZHA backup JSON for addr->IEEE device identification")
	haHost := flag.String("ha-host", "", "Home Assistant host or IP (URL built as ws://<host>:8123/api/websocket)")
	haToken := flag.String("ha-token", "", "Home Assistant long-lived access token")
	configPath := flag.String("config", "zbsniff.yaml", "config file (settings auto-saved and reloaded next run)")
	demoFlag := flag.Bool("demo", false, "start in demo mode with a simulated network (no hardware needed)")
	flag.Parse()

	// Load saved settings; explicit CLI flags win, the file fills the rest.
	set := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { set[f.Name] = true })
	cfg := config.Load(*configPath)
	if !set["channel"] && cfg.Channel > 0 {
		*channel = cfg.Channel
	}
	if !set["db"] && cfg.DB != "" {
		*dbPath = cfg.DB
	}
	if !set["http-port"] && cfg.HTTPPort > 0 {
		*httpPort = cfg.HTTPPort
	}
	if !set["key"] && cfg.Key != "" {
		*keyHex = cfg.Key
	}
	if !set["zha-backup"] && cfg.ZHABackup != "" {
		*zhaBackup = cfg.ZHABackup
	}
	if !set["ha-host"] && cfg.HAHost != "" {
		*haHost = cfg.HAHost
	}
	if !set["ha-token"] && cfg.HAToken != "" {
		*haToken = cfg.HAToken
	}

	// Connection stats + an in-memory log ring for the UI's diagnostics window.
	st := stats.New()
	lb := logbuf.New(400)
	log.SetOutput(io.MultiWriter(os.Stderr, lb))

	db, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}

	var key []byte
	// A ZHA backup carries the network key + channel — use them unless overridden.
	var panID uint16
	// The PAN, once confirmed (by a previous run's ZHA-backup read, or
	// explicitly via the UI's "set as home network" control), takes priority
	// over re-deriving it from the backup file — the backup is a point-in-time
	// snapshot and goes stale if the network's PAN ever changes (Zigbee's own
	// PAN-conflict resolution can do this), which would otherwise silently
	// flip every real device to "foreign" on the next restart with nothing
	// louder than a broken-looking routing tree to notice by.
	if cfg.PanID != 0 {
		panID = cfg.PanID
		log.Printf("PAN id 0x%04x loaded from saved config", panID)
	}
	if *zhaBackup != "" {
		if info, e := names.ReadBackupInfo(*zhaBackup); e == nil {
			if info.NetworkKey != nil {
				key = info.NetworkKey
				log.Printf("network key loaded from ZHA backup")
			}
			if *channel == 0 && info.Channel >= proto.ChannelMin {
				*channel = info.Channel
				log.Printf("channel %d loaded from ZHA backup", info.Channel)
			}
			if panID == 0 && info.PanID != 0 {
				panID = info.PanID
				cfg.PanID = info.PanID // persist — see priority note above
				log.Printf("PAN id 0x%04x loaded from ZHA backup", panID)
			}
		}
	}
	if *keyHex != "" { // explicit --key overrides the backup
		clean := strings.NewReplacer(":", "", "-", "", " ", "", "0x", "").Replace(*keyHex)
		if key, err = hex.DecodeString(clean); err != nil || len(key) != 16 {
			log.Fatalf("network key must be 16 bytes (32 hex chars, separators optional)")
		}
	}
	if key != nil {
		log.Printf("decryption enabled")
	}

	// Fall back to saved ports, then auto-detect the C6 native-USB port.
	// autoDetect=true means we're free to re-discover the port on reconnect (the
	// native-USB port name can change when the C6 resets/re-enumerates).
	explicitPorts := len(ports) > 0
	if len(ports) == 0 && len(cfg.Ports) > 0 {
		ports = cfg.Ports
		log.Printf("using saved port(s): %v", []string(ports))
	}
	if len(ports) == 0 && !*demoFlag {
		if found, _ := serialio.AutoDetect(); len(found) > 0 {
			ports = found
			log.Printf("auto-detected ESP32 USB port(s): %v", []string(ports))
		} else {
			log.Printf("no ESP32 USB device detected yet — will keep looking.")
			log.Printf("available serial ports:\n%s", serialio.DescribePorts())
		}
	}
	autoDetect := !explicitPorts

	rt := runtime.New(key)
	rt.SetPan(panID)
	rad := radios.New()

	// Optional device-name resolution (ZHA backup file + live HA API).
	reg := names.New()
	if *zhaBackup != "" {
		if n, err := reg.LoadZHABackup(*zhaBackup); err != nil {
			log.Printf("zha-backup: %v", err)
		} else {
			log.Printf("zha-backup: loaded %d device addresses", n)
		}
	}
	// Reseed short<->IEEE mappings persisted from previous runs so device
	// names/vendors (incl. Hue) are known immediately, not only after re-hearing.
	for addr, ext := range db.DeviceExts() {
		reg.SetShortExt(uint16(addr), ext)
	}
	ham := names.NewHAManager(reg, func(entries []names.HALogEntry) {
		for _, e := range entries {
			key := fmt.Sprintf("%s@%.0f", e.Logger, e.FirstOccurred)
			db.IngestHALog(key, e.Timestamp, e.FirstOccurred, e.Level, e.Logger, strings.Join(e.Message, " "), e.Exception, e.Count)
		}
	})
	if *haHost != "" && *haToken != "" {
		log.Printf("connecting to Home Assistant at %s", names.WSURLFromHost(*haHost))
		ham.Connect(names.WSURLFromHost(*haHost), *haToken)
	}
	hue := names.NewHueManager(reg)
	if cfg.HueHost != "" && cfg.HueKey != "" {
		log.Printf("connecting to Philips Hue bridge at %s", cfg.HueHost)
		hue.Connect(cfg.HueHost, cfg.HueKey)
	}

	// Keep the resolved settings in the config and persist them (0600).
	cfg.Channel, cfg.DB, cfg.HTTPPort = *channel, *dbPath, *httpPort
	cfg.Key, cfg.ZHABackup = *keyHex, *zhaBackup
	cfg.HAHost, cfg.HAToken, cfg.Ports = *haHost, *haToken, []string(ports)
	var cfgMu sync.Mutex
	saveConfig := func(mut func(*config.Config)) {
		cfgMu.Lock()
		defer cfgMu.Unlock()
		if mut != nil {
			mut(cfg)
		}
		if err := cfg.Save(*configPath); err != nil {
			log.Printf("config save: %v", err)
		}
	}
	prefsSnapshot := func() map[string]string {
		cfgMu.Lock()
		defer cfgMu.Unlock()
		m := make(map[string]string, len(cfg.UIPrefs))
		for k, v := range cfg.UIPrefs {
			m[k] = v
		}
		return m
	}
	saveConfig(nil)
	if abs, err := filepath.Abs(*configPath); err == nil {
		log.Printf("config saved to %s", abs)
	} else {
		log.Printf("config file: %s", *configPath)
	}

	hub := api.NewHub()
	watch := api.NewWatchSet()
	otaProg := api.NewOtaProgress()
	var otaActive atomic.Bool
	var readerPtr atomic.Pointer[serialio.Reader]
	send := func(b []byte) {
		if r := readerPtr.Load(); r != nil {
			r.Send(b)
		}
	}
	sendTo := func(port string, b []byte) bool {
		if r := readerPtr.Load(); r != nil {
			return r.SendTo(port, b)
		}
		return false
	}

	// Scheduler fires recurring active probes (uses the runtime PAN when unset).
	probeFn := func(target, pan int, port string) bool {
		if pan == 0 {
			pan = int(rt.Pan())
		}
		ok := sendTo(port, proto.CmdProbeMsg(uint16(target), uint16(pan)))
		if ok && port != "" {
			rad.Probed(port)
		}
		return ok
	}
	scheduler := sched.New(db, probeFn)
	go scheduler.Run()

	// Radio role assignment. A role's command frames are sent to whichever port
	// currently carries that radio-id; roles persist in the config file.
	var rolesMu sync.Mutex
	roles := parseRoles(cfg.RadioRoles)
	roleFrames := func(role string) [][]byte {
		// "monitor:<ch>" pins a (satellite) radio to a foreign network's channel
		// and captures there — used to watch other networks (e.g. Hue) while the
		// primary stays on your channel.
		if strings.HasPrefix(role, "monitor:") {
			if ch, err := strconv.Atoi(strings.TrimPrefix(role, "monitor:")); err == nil &&
				ch >= proto.ChannelMin && ch <= proto.ChannelMax {
				return [][]byte{proto.CmdSetChannelMsg(byte(ch)),
					proto.CmdSetHopMsg(proto.AllChannelsMask(), 0), // pinned
					proto.CmdSetModeMsg(proto.ModeCapture), proto.CmdStartMsg()}
			}
		}
		switch role {
		case "sniffer", "tester":
			// Deliberately no channel-set here: channel is exclusively the
			// per-radio Ch control's job (see /api/radio_channel). This used
			// to force the channel back to the process's original startup
			// value on every role (re)apply, silently fighting a per-radio
			// channel override — e.g. reassigning "sniffer" to a satellite
			// pinned to a foreign channel would yank it back to the primary's.
			return [][]byte{proto.CmdSetHopMsg(proto.AllChannelsMask(), 0), // 0 = pinned
				proto.CmdSetModeMsg(proto.ModeCapture), proto.CmdStartMsg()}
		case "spectrum":
			return [][]byte{proto.CmdSetHopMsg(proto.AllChannelsMask(), 20),
				proto.CmdSetModeMsg(proto.ModeEdSweep), proto.CmdStartMsg()}
		case "hopper":
			return [][]byte{proto.CmdSetHopMsg(proto.AllChannelsMask(), 300),
				proto.CmdSetModeMsg(proto.ModeCapture), proto.CmdStartMsg()}
		case "idle":
			return [][]byte{proto.CmdSetModeMsg(proto.ModeIdle), proto.CmdStopMsg()}
		}
		return nil
	}
	// satRoleFrames is roleFrames' counterpart for a satellite relayed over SPI
	// (sharing the primary's port — see docs/multi-radio.md). Satellite firmware
	// has no mode/hop/ED concept, only channel + start/stop, so only the roles
	// that reduce to those translate; ok=false means this role can't be applied
	// to a satellite at all (spectrum/hopper need an ED sweep or channel hop).
	satRoleFrames := func(role string, target byte) (frames [][]byte, ok bool) {
		if strings.HasPrefix(role, "monitor:") {
			if ch, err := strconv.Atoi(strings.TrimPrefix(role, "monitor:")); err == nil &&
				ch >= proto.ChannelMin && ch <= proto.ChannelMax {
				return [][]byte{proto.CmdSatSetChannelMsg(target, byte(ch)), proto.CmdSatStartMsg(target)}, true
			}
			return nil, false
		}
		switch role {
		case "sniffer", "tester":
			// No channel-set here either — same reasoning as roleFrames()
			// above: channel is exclusively the per-radio Ch control's job.
			return [][]byte{proto.CmdSatStartMsg(target)}, true
		case "idle":
			return [][]byte{proto.CmdSatStopMsg(target)}, true
		}
		return nil, false
	}
	applyRole := func(radioID int) {
		rolesMu.Lock()
		role := roles[radioID]
		rolesMu.Unlock()
		if role == "" {
			return
		}
		for _, port := range rad.PortsFor(radioID) {
			if rad.IsRelayed(port, radioID) {
				frames, ok := satRoleFrames(role, byte(radioID))
				if !ok {
					log.Printf("radio %d → role %q: satellite firmware can't do this (channel/start-stop only)", radioID, role)
					continue
				}
				for _, f := range frames {
					sendTo(port, f)
				}
				continue
			}
			for _, f := range roleFrames(role) {
				sendTo(port, f)
			}
		}
		log.Printf("radio %d → role %q applied", radioID, role)
	}
	setRole := func(radioID int, role string) {
		rolesMu.Lock()
		if role == "" || role == "none" {
			delete(roles, radioID)
		} else {
			roles[radioID] = role
		}
		snap := formatRoles(roles)
		rolesMu.Unlock()
		saveConfig(func(c *config.Config) { c.RadioRoles = snap })
		applyRole(radioID)
	}
	rolesSnapshot := func() map[int]string {
		rolesMu.Lock()
		defer rolesMu.Unlock()
		m := make(map[int]string, len(roles))
		for k, v := range roles {
			m[k] = v
		}
		return m
	}

	// Demo mode: either forced via --demo, or offered as a choice once the
	// hardware search has gone long enough with nothing found (see
	// noHardwareDetected below) — the dashboard shows a banner with Retry and
	// Enter Demo Mode buttons rather than silently switching over, so a
	// slow-to-enumerate real device is never mistaken for "no hardware".
	var demoActive atomic.Bool
	var noHardwareDetected atomic.Bool
	stopHardwareSearch := make(chan struct{})
	var stopSearchOnce sync.Once
	enterDemoMode := func() {
		if !demoActive.CompareAndSwap(false, true) {
			return // already active
		}
		stopSearchOnce.Do(func() { close(stopHardwareSearch) })
		noHardwareDetected.Store(false)
		rt.SetKey(demo.NetworkKey) // so DecodeFrame actually decrypts simulated home-network traffic
		rt.SetPan(demo.HomePAN)    // so the simulated home network is recognized as "ours", not foreign
		demo.PopulateRegistry(reg)
		src := demo.New()
		src.Run()
		go ingest(src, db, hub, rt, st, rad, reg, watch, otaProg, applyRole)
		log.Printf("demo mode active — simulated network, no hardware required")
	}
	if *demoFlag {
		enterDemoMode()
	}

	// Self-healing connect: retry until the port opens (handles a busy port held
	// by another instance, or the cable being moved to the right connector).
	// This loop runs forever: it (re)discovers the port, connects, and — crucially
	// — reconnects whenever the link drops (C6 reset / USB re-enumeration).
	reconnect := func() {
		if r := readerPtr.Load(); r != nil {
			r.Close() // triggers reader.Done() → the loop below reconnects
		}
	}
	if !*demoFlag {
		go func() {
			searchAttempts := 0
			for {
				select {
				case <-stopHardwareSearch:
					return
				default:
				}
				if len(ports) == 0 {
					if found, _ := serialio.AutoDetect(); len(found) > 0 {
						ports = found
						searchAttempts = 0
						noHardwareDetected.Store(false)
						log.Printf("auto-detected ESP32 USB port(s): %v", []string(ports))
					} else {
						searchAttempts++
						if searchAttempts >= 3 { // ~9s of failed searching
							noHardwareDetected.Store(true)
						}
						select {
						case <-stopHardwareSearch:
							return
						case <-time.After(3 * time.Second):
						}
						continue
					}
				}
				reader := serialio.New(ports, *baud, st)
				if err := reader.Start(); err != nil {
					log.Printf("could not open serial %v: %v — retrying in 3s", ports, err)
					if autoDetect {
						ports = nil // re-discover next iteration
					}
					time.Sleep(3 * time.Second)
					continue
				}
				readerPtr.Store(reader)
				log.Printf("reading from %v — waiting for device data…", []string(ports))
				go ingest(reader, db, hub, rt, st, rad, reg, watch, otaProg, applyRole)
				// Re-send the start sequence a few times (the C6 USB-Serial/JTAG can
				// miss commands right after the port opens) — but only when the
				// primary has no role assigned. Once radio roles are in use,
				// applyRole(0) (triggered via onNewRadio as soon as the primary
				// re-identifies itself) is what configures it; this legacy
				// fallback used a stale, globally-persisted cfg.Mode/*channel from
				// before per-radio roles existed and would silently stomp on
				// whatever role/channel was actually configured for radio 0.
				rolesMu.Lock()
				primaryHasRole := roles[0] != ""
				rolesMu.Unlock()
				if !primaryHasRole {
					mode := byte(proto.ModeCapture)
					if cfg.Mode > 0 {
						mode = byte(cfg.Mode)
					}
					for i := 0; i < 5; i++ {
						if *channel >= proto.ChannelMin && *channel <= proto.ChannelMax {
							reader.Send(proto.CmdSetChannelMsg(byte(*channel)))
						}
						if cfg.HopDwellMs > 0 {
							reader.Send(proto.CmdSetHopMsg(proto.AllChannelsMask(), uint16(cfg.HopDwellMs)))
						}
						reader.Send(proto.CmdSetModeMsg(mode))
						if mode == proto.ModeCapture || mode == proto.ModeCapturePlusEd {
							reader.Send(proto.CmdStartMsg())
						}
						time.Sleep(500 * time.Millisecond)
					}
				}
				<-reader.Done() // block until the link drops (or a manual reconnect)
				log.Printf("device connection lost — reconnecting…")
				reader.Close()
				readerPtr.Store(nil)
				st.Open.Store(false)
				if autoDetect {
					ports = nil // native USB may re-enumerate under a new name
				}
				time.Sleep(1500 * time.Millisecond)
			}
		}()
	}

	// Liveness watchdog. The C6's USB Serial/JTAG link can go quiet WITHOUT the
	// read call erroring (a macOS CDC quirk), so the reader never sees a drop and
	// never reconnects — the UI shows "host up — no device data" until a manual
	// reconnect. If no framed message has arrived for a while despite the port
	// being open, force a reconnect so the link self-heals in a few seconds.
	go func() {
		tick := time.NewTicker(3 * time.Second)
		defer tick.Stop()
		misses := 0
		for range tick.C {
			if !st.Open.Load() {
				continue // not connected → the connect loop is already retrying
			}
			if otaActive.Load() {
				// An active OTA transfer legitimately starves regular STATUS/frame
				// traffic for many seconds (the primary is busy relaying flash-write
				// chunks over SPI to a satellite, or writing its own flash) — that's
				// not a dead link. A watchdog-forced reconnect here tears down the
				// reader mid-transfer and silently aborts the flash write (the
				// device never reaches esp_ota_end()/reboot, so it's left running
				// its old firmware with no visible error).
				misses = 0
				continue
			}
			// Two cases: (a) mid-session silence — data flowed, then stopped
			// (>8s); (b) fresh connect that never produced a byte — the port is
			// open but the device sent nothing since it opened (>15s). Both mean
			// the link is dead and a reconnect may revive it.
			var silent time.Duration
			limit := 8 * time.Second
			if age := st.LastMsgAge(); age > 0 {
				silent = age
			} else {
				silent, limit = st.OpenAge(), 15*time.Second
			}
			if silent > limit {
				// Back off when reconnects keep failing (a physically wedged USB
				// peripheral needs a power cycle — don't spin the log every 8s).
				misses++
				if misses <= 3 || misses%5 == 0 {
					log.Printf("watchdog: no device data for %s — forcing reconnect (attempt %d)", silent.Round(time.Second), misses)
				}
				reconnect()
				time.Sleep(time.Duration(4+min(misses, 8)) * time.Second)
			} else {
				misses = 0
			}
		}
	}()

	// Host-side incident detector. Flags a known device that goes silent past the
	// configured threshold (a dropout) and logs its recovery — this is what
	// populates the Incidents view on the tethered host (firmware-side detection
	// only runs in the standalone build). The threshold is read live from config.
	go func() {
		tick := time.NewTicker(20 * time.Second)
		defer tick.Stop()
		for range tick.C {
			cfgMu.Lock()
			thr := cfg.IncidentSilenceS
			cfgMu.Unlock()
			now := float64(time.Now().UnixNano()) / 1e9
			for _, inc := range db.DetectIncidents(thr, now) {
				ev := map[string]any{"kind": "incident"}
				for k, v := range inc {
					ev[k] = v
				}
				hub.Publish(ev)
				log.Printf("incident: %v went %v (silent %vs)", inc["addr"], inc["reason"], inc["silent_s"])
			}
		}
	}()

	srv := &api.Server{DB: db, Hub: hub, Send: send, Reg: reg, RT: rt, HA: ham, Hue: hue,
		Stats: st, Logs: lb, Web: webui.HTML, SendTo: sendTo, Radios: rad, Watch: watch, OtaActive: &otaActive, OtaProg: otaProg,
		SaveConfig: saveConfig, SetRole: setRole, Roles: rolesSnapshot,
		Reconnect: reconnect, About: aboutInfo(), Prefs: prefsSnapshot,
		Silence:  func() int { cfgMu.Lock(); defer cfgMu.Unlock(); return cfg.IncidentSilenceS },
		Channel:  func() int { cfgMu.Lock(); defer cfgMu.Unlock(); return cfg.Channel },
		HopDwell: func() int { cfgMu.Lock(); defer cfgMu.Unlock(); return cfg.HopDwellMs },
		DemoActive: &demoActive, NoHardwareDetected: &noHardwareDetected, EnterDemo: enterDemoMode}
	addr := ":" + strconv.Itoa(*httpPort)
	log.Printf("zbsniff tethered host on http://localhost%s", addr)
	log.Fatal(http.ListenAndServe(addr, srv.Handler()))
}

// extAddr returns a frame's extended (IEEE) address if it carries one (src
// preferred), and seenPan returns the network PAN the frame belongs to.
func extAddr(m *decode.MacFrame) (uint64, bool) {
	const ext = 3 // addrExtended
	if m.SrcMode == ext && m.SrcAddr >= 0 {
		return uint64(m.SrcAddr), true
	}
	if m.DstMode == ext && m.DstAddr >= 0 {
		return uint64(m.DstAddr), true
	}
	return 0, false
}
func seenPan(m *decode.MacFrame) int {
	if m.DstPan >= 0 && m.DstPan != 0xFFFF {
		return int(m.DstPan)
	}
	if m.SrcPan >= 0 && m.SrcPan != 0xFFFF {
		return int(m.SrcPan)
	}
	return -1
}

// isJoinRelated reports whether a decoded frame looks like part of a
// (re)join handshake rather than ordinary traffic: a raw 802.15.4 MAC command
// (association/orphan), a NWK Rejoin Request/Response, or a ZDO Device_annce.
// isJoinRelated reports whether dec is genuine evidence a device just
// (re)joined the network — NOT just any MAC command frame (a Data Request
// poll fires every cycle, often once a minute, and means nothing on its
// own). Only the specific MAC command subtypes that only occur around a
// join/rejoin, plus the NWK/ZDO equivalents, count.
func isJoinRelated(dec *decode.Decoded) bool {
	if dec.MAC != nil {
		switch dec.MAC.CmdID() {
		case 0x01, 0x02, 0x03, 0x06, 0x08: // Association Req/Resp, Disassociation, Orphan Notification, Coordinator Realignment
			return true
		}
	}
	if dec.NWK != nil && dec.NWK.FrameType == 1 && (dec.NWK.CommandID == 0x06 || dec.NWK.CommandID == 0x07) {
		return true
	}
	if _, _, ok := decode.ZDODeviceAnnounce(dec.APS); ok {
		return true
	}
	return false
}

// msgSource is the minimal interface ingest() needs from a message stream —
// real hardware (*serialio.Reader) and the demo package's simulated *demo.Source
// both satisfy it, so ingest() runs identically over either.
type msgSource interface {
	C() <-chan serialio.Tagged
}

func ingest(r msgSource, db *store.DB, hub *api.Hub, rt *runtime.Runtime, st *stats.Stats, rad *radios.Tracker, reg *names.Registry, watch *api.WatchSet, otaProg *api.OtaProgress, onNewRadio func(int)) {
	first := true
	for t := range r.C() {
		if first {
			first = false
			log.Printf("device data flowing (first message from %s) — capture is live", t.Port)
		}
		st.Touch()
		switch m := t.Msg.(type) {
		case *proto.CapturedFrame:
			st.Frames.Add(1)
			if rad.Saw(t.Port, int(m.RadioID), int(m.Channel), true) {
				onNewRadio(int(m.RadioID))
			}
			dec := decode.DecodeFrame(m.MPDU, rt.Key())
			db.IngestFrame(t.HostTS, int(m.RadioID), int(m.Channel), int(m.RSSI), int(m.LQI), dec, m.MPDU)
			// Label the frame's network by manufacturer (OUI) or a known device
			// name (e.g. Hue) whenever it carries an extended address — this is
			// what turns raw foreign PAN ids into "Philips Hue", "Aqara", etc.
			// Extended addresses come from the MAC header OR the (plaintext) NWK
			// header's src/dst IEEE fields, so this works on foreign networks too.
			if reg != nil {
				// Learn short<->IEEE from the NWK header (carries both), so a
				// device's short address inherits its vendor (OUI) / Hue name.
				learnExt := func(short int, ext uint64) {
					if ext == 0 || !reg.SetShortExt(uint16(short), ext) {
						return
					}
					db.SetDeviceExt(short, ext)
					if nm := reg.NameExt(ext); nm != "" { // e.g. a Hue bridge name
						db.SetDeviceName(short, nm)
					}
				}
				if dec.NWK != nil {
					learnExt(dec.NWK.Src, dec.NWK.SrcExt)
					learnExt(dec.NWK.Dst, dec.NWK.DstExt)
				}
				// ZDO Device_annce (decrypted) is an authoritative short<->IEEE map.
				if nwk, ext, ok := decode.ZDODeviceAnnounce(dec.APS); ok {
					learnExt(int(nwk), ext)
				}
				if pan := seenPan(dec.MAC); pan >= 0 {
					if dec.SixLowPAN {
						// A Thread network's "vendor" (whichever chip made this
						// one frame) isn't a useful network identity the way
						// Hue's OUI is — label the network itself instead of
						// falling through to OUI labeling below.
						db.LabelPan(pan, "Thread network (likely Matter)")
					} else {
						var exts []uint64
						if e, ok := extAddr(dec.MAC); ok {
							exts = append(exts, e)
						}
						if dec.NWK != nil {
							if dec.NWK.SrcExt != 0 {
								exts = append(exts, dec.NWK.SrcExt)
							}
							if dec.NWK.DstExt != 0 {
								exts = append(exts, dec.NWK.DstExt)
							}
						}
						// Networks are identified by MANUFACTURER (OUI) only — never a
						// device name (a bulb's name is not the network's name).
						for _, e := range exts {
							if v := reg.Vendor(e); v != "" {
								db.LabelPan(pan, v)
								break
							}
						}
					}
				}
			}
			if dec.NWK != nil && dec.NWK.IsRouteFailure() {
				tgt := dec.NWK.StatusDest
				if tgt < 0 {
					tgt = dec.NWK.Dst
				}
				db.IngestRouteFailure(tgt, dec.NWK.Src, dec.NWK.StatusReason, t.HostTS)
			}
			hub.Publish(map[string]any{
				"kind": "frame", "ts": t.HostTS, "channel": m.Channel, "radio": m.RadioID,
				"rssi": m.RSSI, "lqi": m.LQI,
				"type": dec.MAC.TypeName, "summary": dec.Summary(),
				"decrypted": dec.NWK != nil && dec.NWK.Decrypted,
				"raw":       hex.EncodeToString(m.MPDU),
			})
			// A genuine (re)join is persisted for EVERY device, not just ones
			// someone happened to be Watching — this is a much stronger, more
			// durable signal than inferring "recovered" from silence timing,
			// and DeviceAnalysis/Diagnostics need it in the DB to correlate
			// against probes/route-failures after the fact, whether or not
			// anyone had the drawer open at the time it happened.
			if dec.MAC.SrcAddr >= 0 && isJoinRelated(dec) {
				db.IngestIncident(map[string]any{
					"ts": t.HostTS, "addr": fmt.Sprintf("0x%04x", uint16(dec.MAC.SrcAddr)),
					"reason": "rejoin", "rssi": m.RSSI, "lqi": m.LQI, "ch": m.Channel,
				})
			}
			if watch != nil {
				srcHex := fmt.Sprintf("0x%04x", uint16(dec.MAC.SrcAddr))
				dstHex := fmt.Sprintf("0x%04x", uint16(dec.MAC.DstAddr))
				addr := ""
				if dec.MAC.SrcAddr >= 0 && watch.Has(srcHex) {
					addr = srcHex
				} else if dec.MAC.DstAddr >= 0 && watch.Has(dstHex) {
					addr = dstHex
				}
				if addr != "" {
					hub.Publish(map[string]any{
						"kind": "watch", "ts": t.HostTS, "addr": addr,
						"channel": m.Channel, "radio": m.RadioID, "rssi": m.RSSI, "lqi": m.LQI,
						"type": dec.MAC.TypeName, "summary": dec.Summary(),
						"join_related": isJoinRelated(dec),
					})
				}
			}
		case *proto.EdResult:
			st.ED.Add(1)
			db.IngestED(t.HostTS, int(m.Channel), int(m.EdDBm), int(m.SweepID), int(m.RadioID))
			hub.Publish(map[string]any{
				"kind": "ed", "channel": m.Channel, "ed_dbm": m.EdDBm, "sweep_id": m.SweepID,
			})
		case *proto.Incident:
			st.Incidents.Add(1)
			if m.Data != nil {
				db.IngestIncident(m.Data)
			}
			ev := map[string]any{"kind": "incident"}
			for k, v := range m.Data {
				ev[k] = v
			}
			hub.Publish(ev)
		case *proto.ProbeResult:
			tgt := fmt.Sprintf("0x%04x", m.Target)
			db.IngestProbe(t.HostTS, int(m.Target), t.Port, int(m.RadioID), m.Acked, int(m.RSSI), int(m.LQI))
			rad.Saw(t.Port, int(m.RadioID), -1, false)
			hub.Publish(map[string]any{
				"kind": "probe", "ts": t.HostTS, "target": tgt, "port": t.Port,
				"radio": m.RadioID, "acked": m.Acked, "rssi": m.RSSI, "lqi": m.LQI,
			})
		case *proto.OtaStatus:
			if otaProg != nil {
				otaProg.Update(int(m.Target), m.Received, m.State)
			}
			hub.Publish(map[string]any{
				"kind": "ota", "target": m.Target, "state": m.State,
				"received": m.Received, "total": m.Total, "err": m.Err, "port": t.Port,
			})
		case *proto.Status:
			st.Status.Add(1)
			if rad.Saw(t.Port, int(m.RadioID), int(m.Channel), false) {
				onNewRadio(int(m.RadioID))
			}
			rad.SetMode(t.Port, int(m.RadioID), int(m.Mode))
			rad.SetUptime(t.Port, int(m.RadioID), int(m.UptimeS))
			modeNames := []string{"idle", "capture", "ed-sweep", "capture+ed"}
			mode := "?"
			if int(m.Mode) < len(modeNames) {
				mode = modeNames[m.Mode]
			}
			st := map[string]any{
				"kind": "status", "channel": m.Channel, "mode": mode, "mode_id": m.Mode,
				"captured": m.Captured, "dropped_buf": m.DroppedBuf, "radio": m.RadioID,
				"uptime_s": m.UptimeS, "hop_dwell_ms": m.HopDwellMs,
				"fw":      fmt.Sprintf("%d.%d", m.FWMajor, m.FWMinor),
				"decrypt": rt.HasKey(),
			}
			rt.SetStatus(st)
			hub.Publish(st)
		}
	}
}

// parseRoles reads "0=sniffer,1=spectrum" into a radio-id → role map.
func parseRoles(s string) map[int]string {
	m := map[int]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		if id, err := strconv.Atoi(strings.TrimSpace(kv[0])); err == nil {
			m[id] = strings.TrimSpace(kv[1])
		}
	}
	return m
}

// formatRoles renders a role map back to "0=sniffer,1=spectrum" (id-sorted).
func formatRoles(m map[int]string) string {
	ids := make([]int, 0, len(m))
	for k := range m {
		ids = append(ids, k)
	}
	sort.Ints(ids)
	var parts []string
	for _, id := range ids {
		if m[id] != "" {
			parts = append(parts, fmt.Sprintf("%d=%s", id, m[id]))
		}
	}
	return strings.Join(parts, ",")
}
