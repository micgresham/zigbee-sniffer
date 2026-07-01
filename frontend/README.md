# Frontend (full web UI)

React + Vite single-page app served by the host app / HA add-on.

## Status

**Scaffold present + working dashboard** (`src/App.jsx`): live device list, RF spectrum bars,
incidents, and routing links, wired to the host REST + WebSocket API. Build it:

```bash
cd frontend
npm install
npm run build        # emits dist/, served by the host app at /
npm run dev          # dev server on :5173, proxies /api + /ws to the host on :8080
```

> Not yet built/verified in CI (no Node in the dev sandbox used to scaffold it). Run the commands
> above to build. The richer per-feature pages below are the next iteration.

Planned pages (see [../docs/api.md](../docs/api.md) for the data contract):

- **Dashboard** — network health summary + active incidents.
- **Devices** — devices in range; per-device RSSI/LQI trends + decoded message log.
- **Routing** — LQI-colored routing tree to the coordinator ([../docs/routing.md](../docs/routing.md)).
- **Inspector** — Wireshark-style decoded frame detail.
- **Spectrum** — ED waterfall/heatmap + Wi-Fi overlap + channel advisor ([../docs/spectrum.md](../docs/spectrum.md)).
- **Incidents** — dropout timeline with RF context + export ([../docs/incidents.md](../docs/incidents.md)).
- **Settings** — channel, network key, hop config, HA integration, thresholds.

The **on-device lite UI** (standalone build, Block B) lives in
[`../firmware/web-lite/`](../firmware/web-lite/) and shares design language with this app while
being far smaller (preact + uPlot, embedded in flash).
