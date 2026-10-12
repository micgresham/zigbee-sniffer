package api

// Wideband SDR spectrum (MODE_SDR_SPECTRUM): start/stop a radio's raw-I/Q
// sweep and serve the stitched traces the ingest loop collects in s.SDR.
// Live traces reach the browser on the WebSocket as {"kind":"sdr", ...Trace}.

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"zbsniff/internal/demo"
	"zbsniff/internal/proto"
	"zbsniff/internal/sdr"
)

// SDRConfig is one radio's sweep plan (mirrors CMD_SDR_CONFIG).
type SDRConfig struct {
	LOs        []uint16 `json:"los"`         // LO per segment, MHz
	FFTLog2    int      `json:"fft_log2"`    // 7 = 128 bins, 8 = 256 bins
	Avg        int      `json:"avg"`         // FFT blocks averaged per LO; 0 = all (63 at 256 bins)
	Detector   string   `json:"detector"`    // "mean" or "peak"
	IntervalMs int      `json:"interval_ms"` // minimum time between sweep starts
}

// DefaultSDRConfig is the stock plan: five Wi-Fi channel centers 15 MHz apart
// covering 2.400–2.4835 GHz. A satellite's 200 kHz SPI link can't keep up with
// back-to-back sweeps (~1.4 KB each), so relayed radios are paced to 4 Hz.
func DefaultSDRConfig(relayed bool) SDRConfig {
	c := SDRConfig{LOs: []uint16{2412, 2427, 2442, 2457, 2472}, FFTLog2: 8, Detector: "mean"}
	if relayed {
		c.IntervalMs = 250
	}
	return c
}

// Frame encodes the plan as a CMD_SDR_CONFIG message.
func (c SDRConfig) Frame() []byte {
	det := byte(proto.SdrDetMean)
	if c.Detector == "peak" {
		det = proto.SdrDetPeak
	}
	return proto.CmdSdrConfigMsg(c.LOs, byte(c.FFTLog2), byte(c.Avg), det, uint16(c.IntervalMs))
}

func (c SDRConfig) validate() error {
	if len(c.LOs) < 1 || len(c.LOs) > 8 {
		return fmt.Errorf("need 1–8 LO frequencies")
	}
	for _, f := range c.LOs {
		if f < 2300 || f > 2600 {
			return fmt.Errorf("LO %d MHz outside 2300–2600", f)
		}
	}
	if c.FFTLog2 < 7 || c.FFTLog2 > 8 {
		return fmt.Errorf("fft_log2 must be 7 or 8")
	}
	if c.Avg < 0 || c.Avg > 63 {
		return fmt.Errorf("avg must be 0–63")
	}
	if c.IntervalMs < 0 || c.IntervalMs > 10000 {
		return fmt.Errorf("interval must be 0–10000 ms")
	}
	return nil
}

// SDREvent wraps a trace as a WebSocket event.
func SDREvent(t *sdr.Trace) any {
	return struct {
		Kind string `json:"kind"`
		*sdr.Trace
	}{"sdr", t}
}

// sdrState remembers what each radio was last asked to sweep.
type sdrState struct {
	mu   sync.Mutex
	cfg  map[int]SDRConfig
	last int // radio most recently started (default for history/stop)
}

var sdrSt = sdrState{cfg: map[int]SDRConfig{}, last: -1}

func (s *Server) demoOn() bool { return s.DemoActive != nil && s.DemoActive.Load() }

// allocateSDR picks a radio for the wideband sweep: one with the "sdr" role,
// else one with the "spectrum" role, else a free (or, with interrupt=true, a
// busy) unassigned radio — in every case only radios whose firmware reports
// CAP_SDR (standalone and pre-SDR firmware don't).
func (s *Server) allocateSDR() (port string, radioID int, ok, interrupt bool, reason string) {
	if s.Radios == nil {
		return "", -1, false, false, "no radios tracked yet"
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
	list := s.Radios.List()
	var capable []int
	for i, r := range list {
		if r.RadioID >= 0 && (r.SDR || s.demoOn()) {
			capable = append(capable, i)
		}
	}
	if len(capable) == 0 {
		return "", -1, false, false, "No radio supports the wideband SDR. It needs ESP32-C6 tethered or " +
			"satellite firmware with SDR support (the standalone build can't — its Wi-Fi AP uses the same PHY)."
	}
	for _, want := range []string{"sdr", "spectrum"} {
		for _, i := range capable {
			if r := list[i]; roleOf(r.RadioID) == want {
				return r.Port, r.RadioID, true, false, ""
			}
		}
	}
	for _, i := range capable { // already sweeping
		if r := list[i]; r.Mode == proto.ModeSdrSpectrum && roleOf(r.RadioID) == "any" {
			return r.Port, r.RadioID, true, false, ""
		}
	}
	var busy int = -1
	for _, i := range capable {
		r := list[i]
		if roleOf(r.RadioID) != "any" {
			continue
		}
		if r.Stopped || r.Mode == proto.ModeIdle || r.Mode == -1 {
			return r.Port, r.RadioID, true, false, ""
		}
		if busy < 0 {
			busy = i
		}
	}
	if busy >= 0 {
		r := list[busy]
		return r.Port, r.RadioID, true, true, fmt.Sprintf(
			"Radio %d is %s. The wideband SDR takes over the whole radio (802.15.4 capture pauses) until you stop it. Continue?",
			r.RadioID, activity(r))
	}
	return "", -1, false, false, "No SDR-capable radio is free (give one the \"sdr\" role in Config → Radios). In use: " +
		describeInUse(list, roleOf)
}

// sdrRadio resolves an explicit ?radio= to its port.
func (s *Server) sdrRadio(id int) (port string, relayed, ok bool) {
	if s.Radios == nil {
		return "", false, false
	}
	for _, r := range s.Radios.List() {
		if r.RadioID == id {
			return r.Port, r.Relayed, true
		}
	}
	return "", false, false
}

func parseSDRConfig(q map[string][]string, base SDRConfig) (SDRConfig, error) {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return strings.TrimSpace(v[0])
		}
		return ""
	}
	c := base
	if v := get("los"); v != "" {
		c.LOs = nil
		for _, f := range strings.Split(v, ",") {
			mhz, err := strconv.Atoi(strings.TrimSpace(f))
			if err != nil {
				return c, fmt.Errorf("bad LO %q", f)
			}
			c.LOs = append(c.LOs, uint16(mhz))
		}
	}
	for k, dst := range map[string]*int{"fft": &c.FFTLog2, "avg": &c.Avg, "interval": &c.IntervalMs} {
		if v := get(k); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return c, fmt.Errorf("bad %s %q", k, v)
			}
			*dst = n
		}
	}
	if v := get("det"); v == "mean" || v == "peak" {
		c.Detector = v
	} else if v != "" {
		return c, fmt.Errorf("det must be mean or peak")
	}
	return c, c.validate()
}

func (s *Server) registerSDR(mux *http.ServeMux) {
	// State: which radios can sweep, which are sweeping, last plans.
	mux.HandleFunc("/api/sdr", func(w http.ResponseWriter, r *http.Request) {
		type radioInfo struct {
			Radio    int    `json:"radio"`
			Port     string `json:"port"`
			Relayed  bool   `json:"relayed"`
			Capable  bool   `json:"capable"`
			Sweeping bool   `json:"sweeping"`
		}
		rs := []radioInfo{}
		if s.Radios != nil {
			for _, rd := range s.Radios.List() {
				if rd.RadioID < 0 {
					continue
				}
				rs = append(rs, radioInfo{rd.RadioID, rd.Port, rd.Relayed, rd.SDR || s.demoOn(),
					rd.Mode == proto.ModeSdrSpectrum})
			}
		}
		sdrSt.mu.Lock()
		cfgs := make(map[string]SDRConfig, len(sdrSt.cfg))
		for id, c := range sdrSt.cfg {
			cfgs[strconv.Itoa(id)] = c
		}
		last := sdrSt.last
		sdrSt.mu.Unlock()
		writeJSON(w, map[string]any{"radios": rs, "configs": cfgs, "last": last,
			"defaults": DefaultSDRConfig(false), "usable_half_mhz": sdr.DefaultUsableHalfMHz})
	})
	// Recent stitched traces (oldest first) to prime the waterfall on page load.
	mux.HandleFunc("/api/sdr/history", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.URL.Query().Get("radio"))
		if err != nil {
			sdrSt.mu.Lock()
			id = sdrSt.last
			sdrSt.mu.Unlock()
			if id < 0 && s.SDR != nil {
				if ids := s.SDR.Radios(); len(ids) > 0 {
					id = ids[len(ids)-1]
				}
			}
		}
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n <= 0 || n > sdr.HistoryLen {
			n = 300
		}
		traces := []*sdr.Trace{}
		if s.SDR != nil && id >= 0 {
			traces = s.SDR.History(id, n)
		}
		writeJSON(w, map[string]any{"radio": id, "traces": traces})
	})
	mux.HandleFunc("/api/sdr/start", func(w http.ResponseWriter, r *http.Request) {
		var (
			port    string
			radioID int
			relayed bool
		)
		if v := r.URL.Query().Get("radio"); v != "" {
			id, err := strconv.Atoi(v)
			p, rel, found := s.sdrRadio(id)
			if err != nil || !found {
				writeJSON(w, map[string]any{"ok": false, "reason": "unknown radio " + v})
				return
			}
			port, radioID, relayed = p, id, rel
		} else {
			p, id, ok, _, reason := s.allocateSDR()
			if !ok {
				writeJSON(w, map[string]any{"ok": false, "reason": reason})
				return
			}
			port, radioID, relayed = p, id, s.relayed(p, id)
		}
		cfg, err := parseSDRConfig(r.URL.Query(), DefaultSDRConfig(relayed))
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "reason": err.Error()})
			return
		}
		sdrSt.mu.Lock()
		sdrSt.cfg[radioID] = cfg
		sdrSt.last = radioID
		sdrSt.mu.Unlock()
		if s.demoOn() {
			demo.StartSdr(radioID, cfg.LOs, cfg.FFTLog2, cfg.Detector == "peak", cfg.IntervalMs)
		} else {
			s.sendToRadio(port, radioID, cfg.Frame(), proto.CmdSetModeMsg(proto.ModeSdrSpectrum))
		}
		if s.Radios != nil {
			s.Radios.SetStopped(port, radioID, false)
		}
		writeJSON(w, map[string]any{"ok": true, "radio": radioID, "config": cfg})
	})
	// Stop hands the radio back to 802.15.4 capture.
	mux.HandleFunc("/api/sdr/stop", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.URL.Query().Get("radio"))
		if err != nil {
			sdrSt.mu.Lock()
			id = sdrSt.last
			sdrSt.mu.Unlock()
		}
		if s.demoOn() {
			demo.StopSdr()
			writeJSON(w, map[string]any{"ok": true, "radio": id})
			return
		}
		port, _, found := s.sdrRadio(id)
		if !found {
			writeJSON(w, map[string]any{"ok": false, "reason": "no SDR radio to stop"})
			return
		}
		s.sendToRadio(port, id, proto.CmdSetModeMsg(proto.ModeCapture), proto.CmdStartMsg())
		writeJSON(w, map[string]any{"ok": true, "radio": id})
	})
	mux.HandleFunc("/api/sdr/clear", func(w http.ResponseWriter, r *http.Request) {
		if s.SDR != nil {
			s.SDR.Clear()
		}
		writeJSON(w, map[string]any{"ok": true})
	})
}
