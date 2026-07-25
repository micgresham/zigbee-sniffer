// Package radios tracks which dongle (serial port) carries which radio, so the
// UI can target a specific radio — e.g. dedicate secondary C6s as active testers
// while the primary keeps sniffing.
package radios

import (
	"sort"
	"strconv"
	"sync"
	"time"
)

// radioIDCollisionBit mirrors RADIO_ID_COLLISION_BIT in firmware/src/core/proto.h.
const radioIDCollisionBit = 0x80

// Radio is one connected radio's last-known identity. Most radios are their own
// USB dongle (Port is that serial port). A satellite reached over SPI has no
// serial port of its own — its messages arrive multiplexed on the primary's
// port, so several Radios can share one Port; Relayed marks that case (see
// docs/multi-radio.md). Relayed radios are commanded via the CMD_SAT_* wire
// commands (target-routed through the primary), not the plain per-port ones.
type Radio struct {
	Port     string  `json:"port"`
	RadioID  int     `json:"radio_id"`
	Channel  int     `json:"channel"`
	Frames   int64   `json:"frames"`
	Probes   int64   `json:"probes"`
	LastSeen float64 `json:"last_seen"`
	Mode     int     `json:"mode"`    // last reported mode (1=capture 2=ed 3=cap+ed 0=idle, -1=unknown)
	Stopped  bool    `json:"stopped"` // user pressed Stop (mode stays 'capture' but RX is off)
	Role     string  `json:"role"`    // filled by the server from the role map
	Relayed  bool    `json:"relayed"` // reached via SPI through another radio's port (a satellite)
	UptimeS  int     `json:"uptime_s"` // last-reported seconds since boot; resets to ~0 across a reboot
}

// Tracker is a thread-safe (port, radio-id)→radio map.
type Tracker struct {
	mu sync.Mutex
	m  map[string]*Radio
}

func New() *Tracker { return &Tracker{m: map[string]*Radio{}} }

func key(port string, radioID int) string {
	if radioID < 0 {
		return port + "|?"
	}
	return port + "|" + strconv.Itoa(radioID)
}

// resolve finds (or creates) the Radio for a concrete (port, radioID) pair.
// Radio ids are fixed by physical SPI slot (see docs/multi-radio.md): 0 is
// always a port's own local radio, and anything else can only be a satellite
// multiplexed in over SPI by the primary's relay — so Relayed is a direct
// function of radioID, not a "first one wins" guess. (An earlier version
// guessed the local radio from whichever radio-id's message arrived first
// after each reconnect, which raced the two radios and could mislabel either
// one — flipping which radio a channel/role change actually landed on.)
// Must be called with t.mu held.
func (t *Tracker) resolve(port string, radioID int) *Radio {
	k := key(port, radioID)
	if r, ok := t.m[k]; ok {
		return r
	}
	// The primary tags a satellite's radio_id with a high bit (see
	// RADIO_ID_COLLISION_BIT in proto.h) when it collides with its own — mask it
	// back off for display so the existing duplicate-id warning in the UI
	// catches it, instead of the two being indistinguishable and merging.
	r := &Radio{Port: port, RadioID: radioID &^ radioIDCollisionBit, Channel: -1, Mode: -1, Relayed: radioID != 0}
	// Drop the port's transient "not yet identified" placeholder now that we
	// have a concrete radio-id for it.
	delete(t.m, key(port, -1))
	t.m[k] = r
	return r
}

// getUnknown returns (creating if needed) the placeholder for a port from which
// nothing has been identified yet.
func (t *Tracker) getUnknown(port string) *Radio {
	k := key(port, -1)
	r, ok := t.m[k]
	if !ok {
		r = &Radio{Port: port, RadioID: -1, Channel: -1, Mode: -1}
		t.m[k] = r
	}
	return r
}

// Saw records traffic from a port (radioID/channel <0 means "unknown, keep
// prior"). It returns true the first time a radio-id is identified on this port
// (including after a reconnect), so the caller can (re)apply that radio's role.
func (t *Tracker) Saw(port string, radioID, channel int, frame bool) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	var r *Radio
	newly := false
	if radioID >= 0 {
		_, existed := t.m[key(port, radioID)]
		r = t.resolve(port, radioID)
		newly = !existed
	} else {
		r = t.getUnknown(port)
	}
	if channel >= 0 {
		r.Channel = channel
	}
	if frame {
		r.Frames++
		r.Stopped = false // frames are arriving → capture is clearly live
	}
	r.LastSeen = float64(time.Now().UnixNano()) / 1e9
	return newly
}

// IsRelayed reports whether (port, radioID) is a satellite reached over SPI
// through the primary sharing that port — such radios take CMD_SAT_* commands,
// not the plain per-port ones which would hit the port's own local radio.
func (t *Tracker) IsRelayed(port string, radioID int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if r, ok := t.m[key(port, radioID)]; ok {
		return r.Relayed
	}
	return false
}

// SetMode records a radio's last-reported operating mode.
func (t *Tracker) SetMode(port string, radioID, mode int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r := t.resolve(port, radioID)
	r.Mode = mode
}

// SetUptime records a radio's last-reported seconds-since-boot.
func (t *Tracker) SetUptime(port string, radioID, uptimeS int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resolve(port, radioID).UptimeS = uptimeS
}

// UptimeOf returns the most recently reported uptime for radioID (across
// whichever port currently carries it) and whether any report exists yet —
// used to detect a reboot (uptime resets to ~0) even when a specific
// confirmation message for it was lost, e.g. after an OTA_END reply drops.
func (t *Tracker) UptimeOf(radioID int) (int, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var best *Radio
	for _, r := range t.m {
		if r.RadioID == radioID && (best == nil || r.LastSeen > best.LastSeen) {
			best = r
		}
	}
	if best == nil {
		return 0, false
	}
	return best.UptimeS, true
}

// SetStopped marks whether the user has stopped a radio's capture. An empty
// port applies to every tracked radio (Stop broadcasts). A negative radioID
// with a non-empty port applies to every radio currently sharing that port
// (back-compat for callers that don't yet know the specific radio-id).
func (t *Tracker) SetStopped(port string, radioID int, stopped bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if port == "" {
		for _, r := range t.m {
			r.Stopped = stopped
		}
		return
	}
	if radioID >= 0 {
		t.resolve(port, radioID).Stopped = stopped
		return
	}
	for _, r := range t.m {
		if r.Port == port {
			r.Stopped = stopped
		}
	}
}

// PortsFor returns the open ports currently reporting the given radio-id.
func (t *Tracker) PortsFor(radioID int) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []string
	for _, r := range t.m {
		if r.RadioID == radioID {
			out = append(out, r.Port)
		}
	}
	return out
}

// Probed bumps the probe counter for a port's local radio (only a port's own
// radio can transmit — a relayed satellite is receive-only). Radio id 0 is
// always a port's own local radio (see resolve).
func (t *Tracker) Probed(port string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resolve(port, 0).Probes++
}

// Reset zeroes every tracked radio's frame/probe counters — used by the
// dashboard's "clear stats" control to start a fresh count without losing
// the radios themselves (identity/role/channel/last-seen are untouched).
func (t *Tracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, r := range t.m {
		r.Frames = 0
		r.Probes = 0
	}
}

// staleAfterS: a radio not heard from (no CapturedFrame/Status/ProbeResult)
// this long is dropped. Both the primary and any satellite heartbeat at ~1Hz,
// so this is generous while still cleaning up a stale identity — e.g. one
// left behind after a reconnect or an old radio_id — within a few seconds
// instead of it lingering forever as a phantom row.
const staleAfterS = 10.0

// List returns the known, still-live radios sorted by port, then radio-id.
// A radio that's gone stale (see staleAfterS) is pruned here, not just hidden,
// so it doesn't linger in memory or block its port/id from being reused.
func (t *Tracker) List() []*Radio {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := float64(time.Now().UnixNano()) / 1e9
	out := make([]*Radio, 0, len(t.m))
	for k, r := range t.m {
		if r.LastSeen > 0 && now-r.LastSeen > staleAfterS {
			delete(t.m, k)
			continue
		}
		c := *r
		out = append(out, &c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].RadioID < out[j].RadioID
	})
	return out
}
