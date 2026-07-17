# Host app

Two hosts exist. The **tethered Go host** ([`tether/`](../tether/)) is the current, actively
developed one: a single static binary (`zbsniff`) that plugs a USB C6 dongle (the `tethered`
firmware) into your PC/HA box, decodes + stores traffic, and serves the web UI. The original
**Python/FastAPI host** ([`host/`](../host/)) still exists for the pcap/CLI tools and the HA
add-on payload; see [the bottom of this page](#python-host-leg--cli--pcap).

## Tethered Go host (`tether/`) — primary

Single-binary pipeline: **serial → decode (+ AES-CCM* decrypt) → SQLite → REST/WebSocket → embedded
web UI** (the UI is compiled in via `go:embed`, so there's one file to ship). No runtime deps.

```bash
cd tether
go build -o zbsniff ./cmd/zbsniff
./zbsniff                                   # uses zbsniff.yaml if present
./zbsniff --port /dev/cu.usbmodem14201 --channel 20 --http-port 8081
# open http://localhost:8081
```

Settings persist to `zbsniff.yaml` (auto-saved when you change them in the UI); secrets are
encrypted at rest (see [Config](#config)). Flags override the file for that run.

### Modules (`tether/internal/`)

| Package | Purpose |
|---------|---------|
| `proto` | wire framing mirror of `firmware/src/core/proto.h` (encode/decode, command builders, CRC-16) |
| `serialio` | opens/merges N serial ports, streams parsed messages; auto-reconnects on drop |
| `decode` | MAC/NWK/APS/ZCL decode + `DecodeFrame()`; AES-CCM* decrypt with the network key |
| `store` | SQLite history + device/link/PAN aggregation; routing, spectrum, diagnostics, **incident detection** queries |
| `api` | REST + WebSocket server (`Hub` fans out live events); serves the embedded UI |
| `names` | address→friendly-name registry (ZHA backup + live HA WebSocket), coordinator LQI |
| `runtime` | mutable network key + latest device status snapshot |
| `radios` | tracks connected dongle identities + roles (sniffer/spectrum/tester/hopper/idle) |
| `sched` | active-testing probe scheduler |
| `stats` | connection/ingest counters + last-message age (feeds the liveness watchdog) |
| `config` | flat YAML load/save; AES-256-GCM encryption of the network key + HA token (sidecar `.key`) |
| `webui` | the embedded `index.html` (served with `Cache-Control: no-cache`) |
| `logbuf` | in-memory host log ring surfaced in the UI |

### Runtime behaviour (`cmd/zbsniff/main.go`)

- **Self-healing connect loop.** Discovers the port, connects, and reconnects on drop (C6 reset /
  USB re-enumeration). Re-sends the start sequence a few times because the C6 USB Serial/JTAG can
  miss commands right after the port opens.
- **Liveness watchdog.** The C6 link can go quiet *without the read erroring* (a macOS CDC quirk);
  if no framed message arrives for ~8 s while the port is open, it forces a reconnect so the link
  self-heals instead of showing "host up — no device data". See [troubleshooting.md](troubleshooting.md).
- **Incident detector.** A goroutine ticks every ~20 s and flags devices that go silent past the
  threshold (and their recovery) — this is what populates the Incidents view on the tethered host.
  See [incidents.md](incidents.md).

### Storage notes (`store/db.go`)

SQLite via `modernc.org/sqlite` (pure Go, no cgo), WAL mode, one connection serialized behind a
mutex. Two things keep it fast under long runs:

- **Indexes** on `ed_samples(ts)`, `ed_samples(channel)`, `packets(ts)`, etc. — without them a
  continuous ED sweep bloats `ed_samples` and the spectrum/diagnostics queries slow to ~seconds,
  which (behind the single mutex) starves ingest and freezes the UI.
- **Retention:** `ed_samples` is capped (~60k rows) with an amortized prune plus a one-time trim on
  open, so a continuous sweep can't grow it without bound.
- **Pre-aggregation:** the Channel-airtime view reads `pan_minutes` — a per-minute rollup
  (frames + RSSI histogram per PAN/channel, 48 h retention) written inline at ingest — precisely
  so it never scans the `packets` table. Heavy ad-hoc queries there are what starve ingest.

### Config

`zbsniff.yaml` is a flat, human-editable key/value file (edit while stopped). Keys include
`channel`, `db`, `http_port`, `ports`, `ha_host`, `hue_host`, `zha_backup`, `radio_roles`,
`hop_dwell_ms`, `mode`, `incident_silence_s`, and `ui.<key>` web-UI preferences. **Secrets are
encrypted at rest:** the Zigbee network `key`, the HA `ha_token`, and the Hue `hue_key` are stored
as `enc:…` (AES-256-GCM) using a sidecar key file (`zbsniff.yaml.key`, 0600). Plaintext values you hand-edit are re-encrypted on the next
save. Don't commit `zbsniff.yaml` / `.key` (they're gitignored).

## Python host (legacy / CLI / pcap)

The original Python package under [`host/`](../host/) provides the framing mirror, the
LINKTYPE_IEEE802_15_4_TAP pcap writer, and CLI tools, and is the HA add-on payload.

```bash
cd host
python3 -m venv .venv && . .venv/bin/activate
pip install -e .          # base (pyserial) — enough for the CLI tools
pip install -e '.[host]'  # full host app deps (fastapi, scapy, cryptography, mqtt, ws)
pip install -e '.[dev]'   # pytest, ruff

python -m zbsniff.tools.listen      --port /dev/ttyACM0 --channel 15
python -m zbsniff.tools.pcap_bridge --port /dev/ttyACM0 --channel 15 --out cap.pcap
python -m pytest -q        # CRC vectors, framing roundtrip, decoder, pcap/TAP output
```

Both CLI tools accept repeated `--port` for multiple radios and are installed as `zbsniff-listen`
and `zbsniff-bridge`.
