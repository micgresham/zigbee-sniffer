package names

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// WSURLFromHost builds a Home Assistant websocket URL from a bare host or IP.
// Accepts "homeassistant.local", "1.2.3.4", "host:8123", "http(s)://host", or a
// full "ws://host/api/websocket" (passed through, with the path appended).
func WSURLFromHost(h string) string {
	h = strings.TrimSpace(h)
	if h == "" {
		return ""
	}
	if strings.HasPrefix(h, "ws://") || strings.HasPrefix(h, "wss://") {
		if !strings.Contains(h, "/api/websocket") {
			h = strings.TrimRight(h, "/") + "/api/websocket"
		}
		return h
	}
	scheme := "ws"
	if strings.HasPrefix(h, "https://") {
		scheme, h = "wss", strings.TrimPrefix(h, "https://")
	}
	h = strings.TrimPrefix(h, "http://")
	if i := strings.IndexByte(h, '/'); i >= 0 {
		h = h[:i] // strip any path
	}
	if !strings.Contains(h, ":") {
		h += ":8123"
	}
	return scheme + "://" + h + "/api/websocket"
}

// HAClient pulls ZHA device names from the Home Assistant WebSocket API.
// url like ws://homeassistant.local:8123/api/websocket
type HAClient struct {
	URL     string
	Token   string
	Reg     *Registry
	LogSink func([]HALogEntry) // optional: receives ZHA/zigpy-sourced system-log entries each Fetch
}

// HALogEntry is one entry from HA's own system_log/list (Settings > Logs) —
// its Python-level warnings/errors, filtered to ZHA/zigpy-sourced ones. This
// is the layer a passive Zigbee sniffer can't see: a command that never made
// it onto the air, a timeout waiting for a response, an exception inside the
// integration — all invisible on the RF side, where nothing looks wrong.
type HALogEntry struct {
	Logger        string   `json:"name"`
	Message       []string `json:"message"`
	Level         string   `json:"level"`
	Timestamp     float64  `json:"timestamp"`
	Exception     string   `json:"exception"`
	Count         int      `json:"count"`
	FirstOccurred float64  `json:"first_occurred"`
}

type haDevice struct {
	IEEE          string          `json:"ieee"`
	NWK           json.RawMessage `json:"nwk"`
	Name          string          `json:"name"`
	UserGivenName string          `json:"user_given_name"`
	LQI           *int            `json:"lqi"`
	RSSI          *int            `json:"rssi"`
	Manufacturer  string          `json:"manufacturer"`
	Model         string          `json:"model"`
	PowerSource   string          `json:"power_source"`
	DeviceType    string          `json:"device_type"`
	Available     *bool           `json:"available"`
	Signature     struct {
		Endpoints map[string]struct {
			Profile json.RawMessage   `json:"profile_id"`
			Type    json.RawMessage   `json:"device_type"`
			In      []json.RawMessage `json:"input_clusters"`
			Out     []json.RawMessage `json:"output_clusters"`
		} `json:"endpoints"`
	} `json:"signature"`
	Neighbors []struct {
		NWK          json.RawMessage `json:"nwk"`
		Relationship string          `json:"relationship"`
		LQI          *int            `json:"lqi"`
	} `json:"neighbors"`
}

// haRegDevice is one entry from HA's config/device_registry/list — a separate,
// core-HA concept from a ZHA device (haDevice above): this is what carries the
// area/room assignment. Cross-referenced to a ZHA device via Identifiers,
// which for a Zigbee device is [["zha", "<ieee>"]].
type haRegDevice struct {
	AreaID      string      `json:"area_id"`
	Identifiers [][2]string `json:"identifiers"`
}

// haArea is one entry from HA's config/area_registry/list.
type haArea struct {
	ID   string `json:"area_id"`
	Name string `json:"name"`
}

// jint parses a ZHA numeric field that may be a JSON number or a "0x.." string.
func jint(raw json.RawMessage) int {
	var num float64
	if json.Unmarshal(raw, &num) == nil {
		return int(num)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		var v int64
		if _, err := fmt.Sscanf(s, "0x%x", &v); err == nil {
			return int(v)
		}
		if _, err := fmt.Sscanf(s, "%d", &v); err == nil {
			return int(v)
		}
	}
	return -1
}
func jints(raws []json.RawMessage) []int {
	out := make([]int, 0, len(raws))
	for _, r := range raws {
		if v := jint(r); v >= 0 {
			out = append(out, v)
		}
	}
	return out
}

// Fetch connects, authenticates, and loads the ZHA device list once.
func (c *HAClient) Fetch(ctx context.Context) (int, error) {
	conn, _, err := websocket.Dial(ctx, c.URL, nil)
	if err != nil {
		return 0, err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(8 << 20)

	read := func() (map[string]any, error) {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return nil, err
		}
		var m map[string]any
		return m, json.Unmarshal(data, &m)
	}
	write := func(v any) error {
		b, _ := json.Marshal(v)
		return conn.Write(ctx, websocket.MessageText, b)
	}

	if _, err := read(); err != nil { // auth_required
		return 0, err
	}
	if err := write(map[string]any{"type": "auth", "access_token": c.Token}); err != nil {
		return 0, err
	}
	authResp, err := read()
	if err != nil {
		return 0, err
	}
	if authResp["type"] != "auth_ok" {
		return 0, fmt.Errorf("auth failed (check the token)")
	}
	if err := write(map[string]any{"id": 1, "type": "zha/devices"}); err != nil {
		return 0, err
	}
	for {
		msg, err := read()
		if err != nil {
			return 0, err
		}
		if msg["type"] != "result" {
			continue
		}
		if ok, _ := msg["success"].(bool); !ok {
			return 0, fmt.Errorf("zha/devices request rejected (is ZHA running?)")
		}
		raw, _ := json.Marshal(msg["result"])
		var devices []haDevice
		json.Unmarshal(raw, &devices)
		n := 0
		for _, d := range devices {
			nwk, ok := parseNWK(d.NWK)
			if !ok {
				continue
			}
			name := d.UserGivenName
			if name == "" {
				name = d.Name
			}
			// Some ZHA entries (often unquirked or partially-supported
			// devices — commonly bare repeaters/range extenders) report both
			// name fields blank. Manufacturer+Model is still far more useful
			// than falling all the way back to a bare IEEE suffix.
			if name == "" {
				name = strings.TrimSpace(d.Manufacturer + " " + d.Model)
			}
			c.Reg.Set(nwk, name)
			// Also key the name by the device's IEEE (stable across a network
			// rejoin, unlike its 16-bit NWK short address — see SetExt). Without
			// this, a device that gets a new short address after rejoining
			// resolves to nothing until the NEXT HA fetch happens to report it
			// under the new address too; keying by IEEE here plus the on-air
			// short->IEEE learning in main.go's learnExt() means it resolves
			// immediately via NameFull()'s ext fallback instead.
			if clean := strings.NewReplacer(":", "", "-", "", " ", "").Replace(d.IEEE); len(clean) == 16 {
				if ext, err := strconv.ParseUint(clean, 16, 64); err == nil {
					c.Reg.SetExt(ext, name)
				}
			}
			if d.LQI != nil || d.RSSI != nil {
				c.Reg.SetNet(nwk, d.LQI, d.RSSI) // nil fields stay absent, not 0
			}
			// Capabilities (endpoints + clusters) as ZHA discovered them.
			info := DeviceInfo{Manufacturer: d.Manufacturer, Model: d.Model,
				PowerSource: d.PowerSource, DeviceType: d.DeviceType}
			if d.Available != nil {
				info.Available, info.HasAvailable = *d.Available, true
			}
			for id, ep := range d.Signature.Endpoints {
				info.Endpoints = append(info.Endpoints, Endpoint{
					ID: jint(json.RawMessage(id)), Profile: jint(ep.Profile), Type: jint(ep.Type),
					In: jints(ep.In), Out: jints(ep.Out)})
			}
			c.Reg.SetInfo(nwk, info)
			// Neighbour table → traceroute topology.
			var nbrs []Neighbor
			for _, nb := range d.Neighbors {
				if v, ok := parseNWK(nb.NWK); ok {
					lqi := 0
					if nb.LQI != nil {
						lqi = *nb.LQI
					}
					nbrs = append(nbrs, Neighbor{NWK: v, Relationship: nb.Relationship, LQI: lqi})
				}
			}
			if len(nbrs) > 0 {
				c.Reg.SetNeighbors(nwk, nbrs)
			}
			n++
		}
		// Best-effort: each device's HA area/room, so the routing graph can
		// label nodes with room names the way HA's own network map does. This
		// is a separate HA-core concept (device registry + area registry) from
		// ZHA's own device list above, needing its own two requests. Not fatal
		// if either fails (older HA core, or a token missing the scope) — the
		// device names/capabilities already fetched above are the essential
		// part; area labels are cosmetic.
		readResult := func() (map[string]any, error) {
			for {
				msg, err := read()
				if err != nil {
					return nil, err
				}
				if msg["type"] == "result" {
					return msg, nil
				}
			}
		}
		if err := write(map[string]any{"id": 2, "type": "config/device_registry/list"}); err == nil {
			if msg, err := readResult(); err == nil {
				if ok, _ := msg["success"].(bool); ok {
					raw, _ := json.Marshal(msg["result"])
					var devReg []haRegDevice
					json.Unmarshal(raw, &devReg)
					if len(devReg) > 0 {
						if err := write(map[string]any{"id": 3, "type": "config/area_registry/list"}); err == nil {
							if msg, err := readResult(); err == nil {
								if ok, _ := msg["success"].(bool); ok {
									raw, _ := json.Marshal(msg["result"])
									var areas []haArea
									json.Unmarshal(raw, &areas)
									areaName := make(map[string]string, len(areas))
									for _, a := range areas {
										areaName[a.ID] = a.Name
									}
									for _, dr := range devReg {
										name := areaName[dr.AreaID]
										if name == "" {
											continue
										}
										for _, ident := range dr.Identifiers {
											if len(ident) != 2 || ident[0] != "zha" {
												continue
											}
											clean := strings.NewReplacer(":", "", "-", "", " ", "").Replace(ident[1])
											if len(clean) != 16 {
												continue
											}
											if ext, err := strconv.ParseUint(clean, 16, 64); err == nil {
												c.Reg.SetArea(ext, name)
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
		// HA's own system log (Settings > Logs), filtered to ZHA/zigpy-sourced
		// entries — same best-effort treatment as area labels above: not fatal
		// if it fails, the device names/capabilities already fetched are the
		// essential part.
		if c.LogSink != nil {
			if err := write(map[string]any{"id": 4, "type": "system_log/list"}); err == nil {
				if msg, err := readResult(); err == nil {
					if ok, _ := msg["success"].(bool); ok {
						raw, _ := json.Marshal(msg["result"])
						var entries []HALogEntry
						json.Unmarshal(raw, &entries)
						var relevant []HALogEntry
						for _, e := range entries {
							lname := strings.ToLower(e.Logger)
							if strings.Contains(lname, "zha") || strings.Contains(lname, "zigpy") {
								relevant = append(relevant, e)
							}
						}
						if len(relevant) > 0 {
							c.LogSink(relevant)
						}
					}
				}
			}
		}
		return n, nil
	}
}

func parseNWK(raw json.RawMessage) (uint16, bool) {
	var num float64
	if json.Unmarshal(raw, &num) == nil {
		return uint16(int(num)), true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		var v uint64
		if _, err := fmt.Sscanf(s, "0x%x", &v); err == nil {
			return uint16(v), true
		}
	}
	return 0, false
}

// HAManager owns the (re)connectable HA link and exposes status to the API.
type HAManager struct {
	reg     *Registry
	mu      sync.Mutex
	cancel  context.CancelFunc
	status  map[string]any
	refresh chan struct{}
	logSink func([]HALogEntry)
}

// NewHAManager creates a manager bound to a name registry. logSink (may be
// nil) receives ZHA/zigpy-sourced HA system-log entries on every fetch cycle
// (currently every 30s — see Connect) for the caller to persist/correlate.
func NewHAManager(reg *Registry, logSink func([]HALogEntry)) *HAManager {
	return &HAManager{reg: reg, status: map[string]any{"connected": false},
		refresh: make(chan struct{}, 1), logSink: logSink}
}

// Refresh triggers an immediate re-fetch from the coordinator (ZHA), refreshing
// device capabilities/neighbours — an on-demand "re-interview" that uses HA's own
// data (no on-air transmitting).
func (m *HAManager) Refresh() {
	select {
	case m.refresh <- struct{}{}:
	default:
	}
}

func (m *HAManager) set(s map[string]any) { m.mu.Lock(); m.status = s; m.mu.Unlock() }

// Status returns the current HA connection status.
func (m *HAManager) Status() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// Connect starts (or restarts) the HA link with the given URL+token.
func (m *HAManager) Connect(url, token string) {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.mu.Unlock()
	m.set(map[string]any{"connected": false, "url": url, "state": "connecting"})

	go func() {
		client := &HAClient{URL: url, Token: token, Reg: m.reg, LogSink: m.logSink}
		for {
			n, err := client.Fetch(ctx)
			if err != nil {
				m.set(map[string]any{"connected": false, "url": url, "error": err.Error()})
				log.Printf("HA integration: %v (retry in 30s)", err)
			} else {
				m.set(map[string]any{"connected": true, "url": url, "devices": n})
				log.Printf("HA integration: %d ZHA device names loaded", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-m.refresh: // manual re-interview → re-fetch now
			case <-time.After(30 * time.Second):
			}
		}
	}()
}
