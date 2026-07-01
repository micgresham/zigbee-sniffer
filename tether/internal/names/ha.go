package names

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
	URL   string
	Token string
	Reg   *Registry
}

type haDevice struct {
	IEEE          string          `json:"ieee"`
	NWK           json.RawMessage `json:"nwk"`
	Name          string          `json:"name"`
	UserGivenName string          `json:"user_given_name"`
	LQI           *int            `json:"lqi"`
	RSSI          *int            `json:"rssi"`
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
			c.Reg.Set(nwk, name)
			if d.LQI != nil || d.RSSI != nil {
				c.Reg.SetNet(nwk, d.LQI, d.RSSI) // nil fields stay absent, not 0
			}
			n++
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
	reg    *Registry
	mu     sync.Mutex
	cancel context.CancelFunc
	status map[string]any
}

// NewHAManager creates a manager bound to a name registry.
func NewHAManager(reg *Registry) *HAManager {
	return &HAManager{reg: reg, status: map[string]any{"connected": false}}
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
		client := &HAClient{URL: url, Token: token, Reg: m.reg}
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
			case <-time.After(30 * time.Second):
			}
		}
	}()
}
