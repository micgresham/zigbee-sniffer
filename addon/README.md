# Home Assistant add-on

Run the **Go tethered host (`zbsniff`)** as a Home Assistant add-on — the whole diagnostics
platform lives inside HA, with the UI in the HA sidebar. Also usable as plain Docker.

- `config.yaml` — add-on manifest: USB/UART mapping, options (serial ports, channel, network
  key, HA host/token), and **ingress** so the UI appears in the HA sidebar (no port to open).
- `build.yaml` — per-arch base images for the Supervisor builder.
- `Dockerfile` — two-stage: compiles the static Go binary, then copies it onto the HA base
  image. Build context is the **repo root** so `tether/` is available.
- `run.sh` — entrypoint; reads `/data/options.json` and launches `zbsniff`.
- `docker-compose.yml` — dev / non-HA deployment of the same binary.

## Install as an HA add-on
1. Settings → Add-ons → Add-on Store → ⋮ → **Repositories** → add this repo's URL.
2. Install **"Zigbee Sniffer & Diagnostics"**.
3. In the add-on **Configuration**: leave `serial_ports` empty to auto-detect the C6, or list the
   dongle path(s); optionally set `channel`, `network_key`, and `ha_host`/`ha_token` (for friendly
   names + Net LQI). Data (SQLite DB + `zbsniff.yaml`) persists on the add-on volume.
4. Start it and open **Zigbee Sniffer** from the sidebar (served via ingress on port 8080).

> Note: the C6 must be flashed with the **tethered** firmware and plugged into the HA host's USB.
> A `usb`+`uart` add-on sees host serial devices; multiple dongles = multiple radios/roles.

## Run with Docker (non-HA)
```bash
cd addon
docker compose up --build      # edit the device path in docker-compose.yml first
# UI at http://<host>:8080
```

See [../docs/deployment-addon.md](../docs/deployment-addon.md).
