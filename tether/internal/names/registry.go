// Package names resolves Zigbee short addresses to human identifiers, sourced
// from a ZHA backup JSON (addr -> IEEE) and/or the live Home Assistant API
// (addr -> friendly name).
package names

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

// NetSig is the network's own view of a device's link (from HA/coordinator).
// ZHA typically reports LQI but not RSSI, so each field tracks its own presence.
type NetSig struct {
	LQI     int
	RSSI    int
	HasLQI  bool
	HasRSSI bool
}

// Registry maps 16-bit short addresses to a display name (IEEE or friendly name)
// and the network-reported link quality (distinct from the sniffer's own view).
type Registry struct {
	mu     sync.RWMutex
	m      map[uint16]string
	net    map[uint16]NetSig
	byExt  map[uint64]string // extended (IEEE) address -> name (e.g. from a Hue bridge)
	extOf  map[uint16]uint64 // short address -> its extended (IEEE) address (learned on-air)
	info   map[uint16]DeviceInfo
	nbr    map[uint16][]Neighbor // per-device neighbour table (from ZHA), for traceroute
	byArea map[uint64]string     // extended (IEEE) address -> HA area/room name
}

// Endpoint describes one application endpoint and its clusters.
type Endpoint struct {
	ID      int   `json:"id"`
	Profile int   `json:"profile"`
	Type    int   `json:"device_type"`
	In      []int `json:"in"`  // input (server) clusters
	Out     []int `json:"out"` // output (client) clusters
}

// DeviceInfo is a device's capabilities as the coordinator (ZHA) discovered them.
type DeviceInfo struct {
	Manufacturer string     `json:"manufacturer"`
	Model        string     `json:"model"`
	PowerSource  string     `json:"power_source"`
	DeviceType   string     `json:"device_type"`
	Available    bool       `json:"available"`     // HA's live reachability view
	HasAvailable bool       `json:"has_available"`  // false if HA didn't report this field at all
	Endpoints    []Endpoint `json:"endpoints"`
}

// Neighbor is one entry of a device's neighbour table (relationship + link LQI).
type Neighbor struct {
	NWK          uint16
	Relationship string // "Parent" / "Child" / "Sibling"
	LQI          int
}

// SetInfo/Info store a device's discovered capabilities.
func (r *Registry) SetInfo(short uint16, i DeviceInfo) {
	r.mu.Lock()
	r.info[short] = i
	r.mu.Unlock()
}
func (r *Registry) Info(short uint16) (DeviceInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	i, ok := r.info[short]
	return i, ok
}

// SetNeighbors records a device's neighbour table; Neighbors snapshots all of them.
func (r *Registry) SetNeighbors(short uint16, ns []Neighbor) {
	r.mu.Lock()
	r.nbr[short] = ns
	r.mu.Unlock()
}
func (r *Registry) Neighbors() map[uint16][]Neighbor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[uint16][]Neighbor, len(r.nbr))
	for k, v := range r.nbr {
		out[k] = v
	}
	return out
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{m: map[uint16]string{}, net: map[uint16]NetSig{},
		byExt: map[uint64]string{}, extOf: map[uint16]uint64{},
		info: map[uint16]DeviceInfo{}, nbr: map[uint16][]Neighbor{},
		byArea: map[uint64]string{}}
}

// Reset clears all learned/cached identity data (names, areas, capabilities,
// neighbour tables, network signal, short<->IEEE mappings) — used by the
// dashboard's "purge data"/"factory reset" controls so a stale mapping (e.g.
// a device's old short address) doesn't linger. HA/Hue will naturally
// repopulate live data on their next fetch cycle.
func (r *Registry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m = map[uint16]string{}
	r.net = map[uint16]NetSig{}
	r.byExt = map[uint64]string{}
	r.extOf = map[uint16]uint64{}
	r.info = map[uint16]DeviceInfo{}
	r.nbr = map[uint16][]Neighbor{}
	r.byArea = map[uint64]string{}
}

// SetShortExt records a short<->extended address mapping, learned from a frame
// that carries both (e.g. the NWK header's source short + source IEEE). This is
// what lets us attach an extended-address name (Hue) or vendor (OUI) to the
// short addresses the rest of the app works in.
// SetShortExt returns true if this is a new/changed mapping (so callers can
// persist it without writing on every frame).
func (r *Registry) SetShortExt(short uint16, ext uint64) bool {
	if ext == 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.extOf[short] == ext {
		return false
	}
	r.extOf[short] = ext
	return true
}

// ExtOf returns the learned extended (IEEE) address for a short address, if
// known — e.g. to match HA/zigpy log lines, which reference devices by IEEE
// far more often than by their (unstable, rejoin-can-change-it) short address.
func (r *Registry) ExtOf(short uint16) (uint64, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ext, ok := r.extOf[short]
	return ext, ok
}

// NameFull returns a real device name for a short address: a friendly name if
// known, else a name for its extended address (e.g. from a Hue bridge), else "".
func (r *Registry) NameFull(hexAddr string) string {
	short, ok := parseShort(hexAddr)
	if !ok {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if n := r.m[short]; n != "" {
		return n
	}
	if ext := r.extOf[short]; ext != 0 {
		return r.byExt[ext]
	}
	return ""
}

// DeviceType returns "Coordinator"/"Router"/"EndDevice" as ZHA reported it for
// this short address, or "" if not (yet) known — e.g. before the first HA
// fetch, or for a foreign-network device HA has never heard of.
func (r *Registry) DeviceType(hexAddr string) string {
	short, ok := parseShort(hexAddr)
	if !ok {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.info[short].DeviceType
}

// Available returns HA's live reachability view for this short address
// (known=false if HA hasn't reported an "available" field for it at all).
func (r *Registry) Available(hexAddr string) (available, known bool) {
	short, ok := parseShort(hexAddr)
	if !ok {
		return false, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	i := r.info[short]
	return i.Available, i.HasAvailable
}

// Mfg returns the manufacturer (OUI) label for a short address, via its learned
// extended address, or "". Used as a fallback when no name is available.
func (r *Registry) Mfg(hexAddr string) string {
	short, ok := parseShort(hexAddr)
	if !ok {
		return ""
	}
	r.mu.RLock()
	ext := r.extOf[short]
	r.mu.RUnlock()
	if ext == 0 {
		return ""
	}
	return vendorForExt(ext)
}

// SetArea records a device's HA area/room name, keyed by its extended (IEEE)
// address (from HA's device_registry + area_registry, cross-referenced by
// IEEE — see ha.go). Robust to the device's short address changing later, the
// same way SetExt/byExt already is.
func (r *Registry) SetArea(ext uint64, area string) {
	if area == "" || ext == 0 {
		return
	}
	r.mu.Lock()
	r.byArea[ext] = area
	r.mu.Unlock()
}

// Area returns the HA area/room name for a short address, via its learned
// extended address, or "" if unknown.
func (r *Registry) Area(hexAddr string) string {
	short, ok := parseShort(hexAddr)
	if !ok {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byArea[r.extOf[short]]
}

func parseShort(hexAddr string) (uint16, bool) {
	v, err := strconv.ParseUint(strings.TrimPrefix(hexAddr, "0x"), 16, 16)
	if err != nil {
		return 0, false
	}
	return uint16(v), true
}

// SetExt records a name for an extended (IEEE) address. Extended addresses are
// globally unique, so this is unambiguous across networks (unlike short
// addresses). Used by integrations that know a device by its MAC (e.g. Hue).
func (r *Registry) SetExt(ext uint64, name string) {
	if name == "" || ext == 0 {
		return
	}
	r.mu.Lock()
	r.byExt[ext] = name
	r.mu.Unlock()
}

// NameExt returns the friendly name for an extended address, or "".
func (r *Registry) NameExt(ext uint64) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byExt[ext]
}

// Vendor returns the manufacturer label for an extended address from its OUI
// (top 3 bytes), or "".
func (r *Registry) Vendor(ext uint64) string { return vendorForExt(ext) }

// LabelExt returns the best label for an extended address: a known friendly
// name (integration) if present, else the manufacturer (OUI), else "".
func (r *Registry) LabelExt(ext uint64) string {
	if n := r.NameExt(ext); n != "" {
		return n
	}
	return vendorForExt(ext)
}

// Set records a name for a short address (later/friendlier sources overwrite).
func (r *Registry) Set(short uint16, name string) {
	if name == "" {
		return
	}
	r.mu.Lock()
	r.m[short] = name
	r.mu.Unlock()
}

// SetNet records the network-reported (coordinator-side) LQI/RSSI for a device.
// nil means "not reported by HA" and is stored as absent (not zero).
func (r *Registry) SetNet(short uint16, lqi, rssi *int) {
	s := NetSig{}
	if lqi != nil {
		s.LQI, s.HasLQI = *lqi, true
	}
	if rssi != nil {
		s.RSSI, s.HasRSSI = *rssi, true
	}
	r.mu.Lock()
	r.net[short] = s
	r.mu.Unlock()
}

// Net returns the network-reported signal for a short address (hex string).
func (r *Registry) Net(hexAddr string) (NetSig, bool) {
	v, err := strconv.ParseUint(strings.TrimPrefix(hexAddr, "0x"), 16, 16)
	if err != nil {
		return NetSig{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.net[uint16(v)]
	return s, ok && (s.HasLQI || s.HasRSSI)
}

// Name returns the display name for a short address (hex string "0x1234"), or "".
func (r *Registry) Name(hexAddr string) string {
	v, err := strconv.ParseUint(strings.TrimPrefix(hexAddr, "0x"), 16, 16)
	if err != nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.m[uint16(v)]
}

type zhaBackup struct {
	NetworkInfo struct {
		Channel      int               `json:"channel"`
		PanID        string            `json:"pan_id"` // "0x1A2B" or "1A2B"
		NwkAddresses map[string]string `json:"nwk_addresses"` // "ieee": "E30F"
		NetworkKey   struct {
			Key string `json:"key"` // "6e:c1:92:..."
		} `json:"network_key"`
	} `json:"network_info"`
}

// BackupInfo is the bits of a ZHA backup useful for capture setup.
type BackupInfo struct {
	Channel    int
	PanID      uint16 // network PAN id (0 if absent)
	NetworkKey []byte // 16 bytes, or nil
}

// ReadBackupInfo extracts the channel + network key from a ZHA backup JSON.
func ReadBackupInfo(path string) (BackupInfo, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return BackupInfo{}, err
	}
	var bk zhaBackup
	if err := json.Unmarshal(b, &bk); err != nil {
		return BackupInfo{}, err
	}
	info := BackupInfo{Channel: bk.NetworkInfo.Channel}
	if ps := strings.TrimPrefix(strings.ToLower(bk.NetworkInfo.PanID), "0x"); ps != "" {
		if v, err := strconv.ParseUint(ps, 16, 16); err == nil {
			info.PanID = uint16(v)
		}
	}
	clean := strings.NewReplacer(":", "", "-", "", " ", "").Replace(bk.NetworkInfo.NetworkKey.Key)
	if len(clean) == 32 {
		if raw, err := hexDecode(clean); err == nil {
			info.NetworkKey = raw
		}
	}
	return info, nil
}

func hexDecode(s string) ([]byte, error) {
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		v, err := strconv.ParseUint(s[i*2:i*2+2], 16, 8)
		if err != nil {
			return nil, err
		}
		out[i] = byte(v)
	}
	return out, nil
}

// LoadZHABackup loads a ZHA/zigpy backup JSON, mapping each short address to a
// short form of its IEEE address (used until the HA API supplies a friendly name).
func (r *Registry) LoadZHABackup(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var bk zhaBackup
	if err := json.Unmarshal(b, &bk); err != nil {
		return 0, err
	}
	n := 0
	for ieee, nwkHex := range bk.NetworkInfo.NwkAddresses {
		nwk, err := strconv.ParseUint(strings.TrimPrefix(nwkHex, "0x"), 16, 16)
		if err != nil {
			continue
		}
		// e.g. "…b1:b0" — last two octets of the IEEE, recognisable without HA.
		short := ieee
		if parts := strings.Split(ieee, ":"); len(parts) >= 2 {
			short = fmt.Sprintf("…%s:%s", parts[len(parts)-2], parts[len(parts)-1])
		}
		r.Set(uint16(nwk), short)
		// Record the full IEEE so our own devices resolve their manufacturer (OUI)
		// immediately, without waiting to overhear an IEEE-bearing frame.
		if clean := strings.NewReplacer(":", "", "-", "", " ", "").Replace(ieee); len(clean) == 16 {
			if ext, err := strconv.ParseUint(clean, 16, 64); err == nil {
				r.SetShortExt(uint16(nwk), ext)
			}
		}
		n++
	}
	return n, nil
}
