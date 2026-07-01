# Deployment — Home Assistant add-on

> Status: design. Built in Block C (phase 9). A `docker compose` dev path comes with it (an HA
> add-on is just a container).

## As a Home Assistant add-on (primary)

Packaged under [`addon/`](../addon/):
- `config.yaml` — add-on manifest: name, slug, USB device mapping (`uart`/`usb`), options schema
  (channel, network key, HA integration toggle, incident thresholds), ingress for the web UI.
- `Dockerfile` — builds the host backend + the compiled React frontend into one image.
- `run.sh` — entrypoint (reads add-on options → env, starts uvicorn).

Install flow: add this repo as a custom add-on repository in the HA Supervisor → install →
configure → the UI appears via **ingress** in the HA sidebar. The add-on can reach HA's API for
[ZHA/Z2M integration](ha-integration.md) and map the sniffer's USB device through.

## As plain Docker / docker-compose (dev or non-HA hosts)

```bash
cd addon
docker compose up --build
```
Mounts the sniffer serial device, persists the SQLite DB to a volume, serves the UI on a local
port. Same image as the add-on, just launched directly.

## Bare host (Raspberry Pi / Linux)

```bash
cd host && pip install -e '.[host]'
uvicorn zbsniff.api.rest:app --host 0.0.0.0 --port 8080
```
Optionally a systemd unit for an always-on diagnostic appliance near your coordinator.

## USB device mapping notes

- One radio: map the single serial device.
- Multiple radios / carrier: map each, or the USB hub — the host auto-detects and merges.
- On HA OS, USB devices appear under `/dev/serial/by-id/…`; prefer the stable by-id path.
