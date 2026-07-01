// Package radios tracks which dongle (serial port) carries which radio, so the
// UI can target a specific radio — e.g. dedicate secondary C6s as active testers
// while the primary keeps sniffing.
package radios

import (
	"sort"
	"sync"
	"time"
)

// Radio is one connected dongle's last-known identity.
type Radio struct {
	Port     string  `json:"port"`
	RadioID  int     `json:"radio_id"`
	Channel  int     `json:"channel"`
	Frames   int64   `json:"frames"`
	Probes   int64   `json:"probes"`
	LastSeen float64 `json:"last_seen"`
	Mode     int     `json:"mode"` // last reported mode (1=capture 2=ed 3=cap+ed 0=idle, -1=unknown)
	Role     string  `json:"role"` // filled by the server from the role map
}

// Tracker is a thread-safe port→radio map.
type Tracker struct {
	mu sync.Mutex
	m  map[string]*Radio
}

func New() *Tracker { return &Tracker{m: map[string]*Radio{}} }

func (t *Tracker) get(port string) *Radio {
	r, ok := t.m[port]
	if !ok {
		r = &Radio{Port: port, RadioID: -1, Channel: -1, Mode: -1}
		t.m[port] = r
	}
	return r
}

// SetMode records a radio's last-reported operating mode.
func (t *Tracker) SetMode(port string, mode int) {
	t.mu.Lock()
	t.get(port).Mode = mode
	t.mu.Unlock()
}

// Saw records traffic from a port (radioID/channel <0 means "unknown, keep
// prior"). It returns true the first time a radio-id is identified on this port
// (including after a reconnect), so the caller can (re)apply that radio's role.
func (t *Tracker) Saw(port string, radioID, channel int, frame bool) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	r := t.get(port)
	newly := false
	if radioID >= 0 && r.RadioID != radioID {
		r.RadioID = radioID
		newly = true
	}
	if channel >= 0 {
		r.Channel = channel
	}
	if frame {
		r.Frames++
	}
	r.LastSeen = float64(time.Now().UnixNano()) / 1e9
	return newly
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

// Probed bumps the probe counter for a port.
func (t *Tracker) Probed(port string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.get(port).Probes++
}

// List returns the known radios sorted by port.
func (t *Tracker) List() []*Radio {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*Radio, 0, len(t.m))
	for _, r := range t.m {
		c := *r
		out = append(out, &c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}
