// Package names — Philips Hue bridge integration. A Hue bridge is its own Zigbee
// coordinator on its own PAN/channel. Its local API exposes each device's name +
// extended (IEEE) MAC address and the bridge's Zigbee channel, which we feed into
// the registry (by extended address) so Hue devices/networks get real names.
//
// Auth uses the standard link-button flow: POST to /api creates an application
// key AFTER the physical link button is pressed. The bridge serves HTTPS with a
// self-signed cert, so we skip verification (local LAN, bridge-id-signed cert).
package names

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A Hue application key is embedded in the request URL path (/api/<key>/…), so
// Go's http errors carry it verbatim. Scrub it from anything we log or expose
// (the log ring feeds the UI's logs panel, and shows up in screenshots/exports)
// — the key grants full control of the bridge.
var hueKeyRe = regexp.MustCompile(`/api/[^/\s"]+`)

func redactHueKey(s string) string { return hueKeyRe.ReplaceAllString(s, "/api/***") }

var hueClient = &http.Client{
	Timeout:   8 * time.Second,
	Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
}

// HuePair performs the link-button pairing: the user presses the bridge's link
// button, then this creates and returns an application key. A friendly error is
// returned if the button hasn't been pressed yet.
func HuePair(host string) (string, error) {
	resp, err := hueClient.Post("https://"+hostOnly(host)+"/api", "application/json",
		strings.NewReader(`{"devicetype":"zbsniff#host"}`))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var out []struct {
		Success struct {
			Username string `json:"username"`
		} `json:"success"`
		Error struct {
			Type        int    `json:"type"`
			Description string `json:"description"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil || len(out) == 0 {
		return "", fmt.Errorf("unexpected bridge response")
	}
	if out[0].Success.Username != "" {
		return out[0].Success.Username, nil
	}
	if out[0].Error.Description != "" {
		return "", fmt.Errorf("%s", out[0].Error.Description) // e.g. "link button not pressed"
	}
	return "", fmt.Errorf("pairing failed")
}

// HueManager holds the live Hue connection state and pushes names into the registry.
type HueManager struct {
	reg    *Registry
	mu     sync.Mutex
	status map[string]any
	cancel context.CancelFunc
}

func NewHueManager(reg *Registry) *HueManager {
	return &HueManager{reg: reg, status: map[string]any{"connected": false}}
}

func (m *HueManager) set(s map[string]any) { m.mu.Lock(); m.status = s; m.mu.Unlock() }

func (m *HueManager) Status() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := map[string]any{}
	for k, v := range m.status {
		s[k] = v
	}
	return s
}

// Connect starts polling the bridge for device names (every 30s) until the next
// Connect call. An empty host/key just clears the connection.
func (m *HueManager) Connect(host, key string) {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.mu.Unlock()
	if host == "" || key == "" {
		m.set(map[string]any{"connected": false})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()
	m.set(map[string]any{"connected": false, "host": host, "state": "connecting"})

	go func() {
		for {
			n, ch, err := hueFetch(hostOnly(host), key, m.reg)
			if err != nil {
				safe := redactHueKey(err.Error())
				m.set(map[string]any{"connected": false, "host": host, "error": safe})
				log.Printf("Hue integration: %s (retry in 30s)", safe)
			} else {
				m.set(map[string]any{"connected": true, "host": host, "devices": n, "channel": ch})
				log.Printf("Hue integration: %d device names loaded (bridge ch %d)", n, ch)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
		}
	}()
}

// hueFetch pulls lights + sensors (each carries a "uniqueid" = MAC) and the
// bridge's zigbee channel, feeding extended-address→name into the registry.
// Returns (devices named, bridge channel).
func hueFetch(host, key string, reg *Registry) (int, int, error) {
	base := "https://" + host + "/api/" + key
	named := 0
	for _, res := range []string{"/lights", "/sensors"} {
		items := map[string]struct {
			Name     string `json:"name"`
			UniqueID string `json:"uniqueid"`
		}{}
		if err := hueGet(base+res, &items); err != nil {
			return 0, 0, err
		}
		for _, it := range items {
			if ext, ok := macToExt(it.UniqueID); ok && it.Name != "" {
				reg.SetExt(ext, it.Name)
				named++
			}
		}
	}
	var cfg struct {
		ZigbeeChannel int `json:"zigbeechannel"`
	}
	hueGet(base+"/config", &cfg) // best-effort; channel is a nice-to-have
	return named, cfg.ZigbeeChannel, nil
}

func hueGet(url string, v any) error {
	resp, err := hueClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("bridge %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// macToExt parses a Hue "uniqueid" ("00:17:88:01:04:ab:cd:ef-0b-1000") into a
// 64-bit extended address.
func macToExt(uid string) (uint64, bool) {
	if i := strings.IndexByte(uid, '-'); i > 0 {
		uid = uid[:i]
	}
	hex := strings.ReplaceAll(uid, ":", "")
	if len(hex) != 16 {
		return 0, false
	}
	v, err := strconv.ParseUint(hex, 16, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// hostOnly strips any scheme/path a user may have pasted, leaving host[:port].
func hostOnly(h string) string {
	h = strings.TrimSpace(h)
	h = strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://")
	if i := strings.IndexAny(h, "/"); i >= 0 {
		h = h[:i]
	}
	return h
}
