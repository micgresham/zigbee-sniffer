// Package webui embeds the tethered-mode web UI (single self-contained page).
package webui

import _ "embed"

//go:embed index.html
var HTML []byte
