// Package config persists host settings to a small human-readable YAML file so
// they reload next run (HA host/token, channel, key, ports…). It hand-rolls a
// flat key: value reader/writer to avoid a YAML dependency — the schema is flat.
package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config is the persisted host configuration.
type Config struct {
	HAHost    string   // Home Assistant host or IP (URL is built from this)
	HAToken   string   // long-lived access token
	Channel   int      // capture channel 11..26
	Key       string   // Zigbee network key (hex)
	DB        string   // SQLite path
	HTTPPort  int      // web UI port
	Ports     []string // serial ports
	ZHABackup string   // ZHA backup JSON path
	// RadioRoles maps radio-id → role, e.g. "0=sniffer,1=spectrum,2=tester".
	RadioRoles string
}

// Load reads a config file. A missing file yields an empty Config (no error).
func Load(path string) *Config {
	c := &Config{}
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
		case "radio_roles":
			c.RadioRoles = v
		case "ports":
			for _, p := range strings.Split(v, ",") {
				if p = strings.TrimSpace(p); p != "" {
					c.Ports = append(c.Ports, p)
				}
			}
		}
	}
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
	w("ha_token", c.HAToken)
	if c.Channel > 0 {
		fmt.Fprintf(&b, "channel: %d\n", c.Channel)
	}
	w("key", c.Key)
	w("db", c.DB)
	if c.HTTPPort > 0 {
		fmt.Fprintf(&b, "http_port: %d\n", c.HTTPPort)
	}
	w("zha_backup", c.ZHABackup)
	w("radio_roles", c.RadioRoles)
	if len(c.Ports) > 0 {
		w("ports", strings.Join(c.Ports, ","))
	}
	return os.WriteFile(path, []byte(b.String()), 0600)
}

func quote(v string) string {
	if strings.ContainsAny(v, ":#,\"") || strings.HasPrefix(v, " ") {
		return `"` + strings.ReplaceAll(v, `"`, "") + `"`
	}
	return v
}
