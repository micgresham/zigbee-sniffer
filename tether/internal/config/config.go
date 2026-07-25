// Package config persists host settings to a small human-readable YAML file so
// they reload next run (HA host/token, channel, key, ports…). It hand-rolls a
// flat key: value reader/writer to avoid a YAML dependency — the schema is flat.
package config

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Config is the persisted host configuration.
type Config struct {
	HAHost    string   // Home Assistant host or IP (URL is built from this)
	HAToken   string   // long-lived access token (encrypted at rest)
	HueHost   string   // Philips Hue bridge host or IP
	HueKey    string   // Hue application key (encrypted at rest)
	Channel   int      // capture channel 11..26
	Key       string   // Zigbee network key (hex; encrypted at rest)
	DB        string   // SQLite path
	HTTPPort  int      // web UI port
	Ports     []string // serial ports
	ZHABackup string   // ZHA backup JSON path
	// PanID is the home network's PAN, explicitly confirmed (via the UI's "set
	// as home network" control) or first learned from a ZHA backup file. Once
	// set, it takes priority over re-deriving from the backup file on a later
	// run — the backup is a point-in-time snapshot and goes stale if the
	// network's PAN ever changes (e.g. an automatic Zigbee PAN-conflict
	// resolution), which would otherwise silently flip every real device to
	// "foreign" with no way to notice except a broken-looking routing tree.
	PanID uint16
	// RadioRoles maps radio-id → role, e.g. "0=sniffer,1=spectrum,2=tester".
	RadioRoles string
	HopDwellMs int // channel-hop dwell (0 = pinned)
	Mode       int // capture mode (1=capture 2=ed 3=cap+ed 0=idle)
	// IncidentSilenceS: a known device silent longer than this (seconds) logs a
	// "silence" incident (0 → use the default). See analytics/incidents detector.
	IncidentSilenceS int
	// UIPrefs holds web-UI preferences (theme, routing spacing, filters…), stored
	// as `ui.<key>: <value>` lines so they persist and are human-editable.
	UIPrefs map[string]string

	secretKey []byte // AES key for encrypting HAToken/Key at rest
}

// Load reads a config file. A missing file yields an empty Config (no error).
// Secrets (network key, HA token) are decrypted using the sidecar key file.
func Load(path string) *Config {
	c := &Config{secretKey: loadOrCreateKey(path + ".key"), UIPrefs: map[string]string{}}
	f, err := os.Open(path)
	if err != nil {
		return c
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"`)
		switch k {
		case "ha_host":
			c.HAHost = v
		case "ha_token":
			c.HAToken = v
		case "hue_host":
			c.HueHost = v
		case "hue_key":
			c.HueKey = v
		case "channel":
			c.Channel, _ = strconv.Atoi(v)
		case "key":
			c.Key = v
		case "db":
			c.DB = v
		case "http_port":
			c.HTTPPort, _ = strconv.Atoi(v)
		case "zha_backup":
			c.ZHABackup = v
		case "pan":
			p, _ := strconv.ParseUint(strings.TrimPrefix(v, "0x"), 16, 16)
			c.PanID = uint16(p)
		case "radio_roles":
			c.RadioRoles = v
		case "hop_dwell_ms":
			c.HopDwellMs, _ = strconv.Atoi(v)
		case "mode":
			c.Mode, _ = strconv.Atoi(v)
		case "incident_silence_s":
			c.IncidentSilenceS, _ = strconv.Atoi(v)
		case "ports":
			for _, p := range strings.Split(v, ",") {
				if p = strings.TrimSpace(p); p != "" {
					c.Ports = append(c.Ports, p)
				}
			}
		default:
			if strings.HasPrefix(k, "ui.") {
				c.UIPrefs[strings.TrimPrefix(k, "ui.")] = v
			}
		}
	}
	// Decrypt secrets that were stored as "enc:…" (plaintext values pass through
	// and get encrypted on the next Save — e.g. a hand-edited key).
	c.HAToken = decryptField(c.secretKey, c.HAToken)
	c.Key = decryptField(c.secretKey, c.Key)
	c.HueKey = decryptField(c.secretKey, c.HueKey)
	return c
}

// Save writes the config file (0600 — it holds the HA token).
func (c *Config) Save(path string) error {
	var b strings.Builder
	b.WriteString("# zbsniff configuration — auto-saved; edit while stopped.\n")
	w := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%s: %s\n", k, quote(v))
		}
	}
	w("ha_host", c.HAHost)
	w("ha_token", encryptField(c.secretKey, c.HAToken)) // encrypted at rest
	w("hue_host", c.HueHost)
	w("hue_key", encryptField(c.secretKey, c.HueKey)) // encrypted at rest
	if c.Channel > 0 {
		fmt.Fprintf(&b, "channel: %d\n", c.Channel)
	}
	w("key", encryptField(c.secretKey, c.Key)) // encrypted at rest
	w("db", c.DB)
	if c.HTTPPort > 0 {
		fmt.Fprintf(&b, "http_port: %d\n", c.HTTPPort)
	}
	w("zha_backup", c.ZHABackup)
	if c.PanID != 0 {
		fmt.Fprintf(&b, "pan: 0x%04x\n", c.PanID)
	}
	w("radio_roles", c.RadioRoles)
	if c.HopDwellMs > 0 {
		fmt.Fprintf(&b, "hop_dwell_ms: %d\n", c.HopDwellMs)
	}
	if c.Mode > 0 {
		fmt.Fprintf(&b, "mode: %d\n", c.Mode)
	}
	if c.IncidentSilenceS > 0 {
		fmt.Fprintf(&b, "incident_silence_s: %d\n", c.IncidentSilenceS)
	}
	if len(c.Ports) > 0 {
		w("ports", strings.Join(c.Ports, ","))
	}
	keys := make([]string, 0, len(c.UIPrefs))
	for k := range c.UIPrefs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		w("ui."+k, c.UIPrefs[k])
	}
	return os.WriteFile(path, []byte(b.String()), 0600)
}

func quote(v string) string {
	if strings.ContainsAny(v, ":#,\"") || strings.HasPrefix(v, " ") {
		return `"` + strings.ReplaceAll(v, `"`, "") + `"`
	}
	return v
}
