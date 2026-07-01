# Home Assistant add-on

Run the host app as an HA add-on (and as plain Docker). **Implemented (Block C).**

- `config.yaml` — add-on manifest: USB/UART mapping, options (serial ports, channel, network
  key, decrypt, silence threshold), and **ingress** so the UI appears in the HA sidebar.
- `Dockerfile` — host backend (+ built React UI when present) in one image. Build context is the
  **repo root** so `host/` and `frontend/dist/` are available.
- `run.sh` — entrypoint; reads `/data/options.json` and launches `zbsniff.api.server`.
- `docker-compose.yml` — dev / non-HA deployment of the same image.

## Install as an HA add-on
1. Settings → Add-ons → Add-on Store → ⋮ → Repositories → add this repo's URL.
2. Install "Zigbee Sniffer & Diagnostics", set your serial port(s) and channel (and network key
   if you want decryption), start it, and open it from the sidebar (ingress).

## Run with Docker (non-HA)
```bash
cd addon
docker compose up --build      # edit device path + channel in docker-compose.yml first
```

See [../docs/deployment-addon.md](../docs/deployment-addon.md).
