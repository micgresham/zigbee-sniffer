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
	mu    sync.RWMutex
	m     map[uint16]string
	net   map[uint16]NetSig
	byExt map[uint64]string // extended (IEEE) address -> name (e.g. from a Hue bridge)
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{m: map[uint16]string{}, net: map[uint16]NetSig{}, byExt: map[uint64]string{}}
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
		n++
	}
	return n, nil
}
