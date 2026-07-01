# Routing tree reconstruction

> Status: **on-device MVP implemented (Block B)** — the lite UI reconstructs a link graph from
> observed MAC src→dst pairs and lays it out radially by hop-distance from the coordinator
> (0x0000), edges colored by LQI. This is the *observed-relaying + per-link RSSI/LQI* data source
> below. Full NWK-based routing (route requests/replies, source routes) needs the network key and
> lands with NWK decode in Block C; coordinator-map cross-reference comes with HA integration.

## Goal

Show how each device reaches the coordinator — a graph of parent/child and mesh routes with
edges colored by link quality — so you can spot devices on a weak/long path that are prone to
dropping.

## Data sources

1. **Sniffed NWK layer** (needs the network key to read addressing reliably — see
   [decoding-and-decryption.md](decoding-and-decryption.md)):
   - NWK **Route Request / Route Reply** command frames reveal route discovery.
   - NWK **source-route** subframes list the relay path explicitly.
   - **Observed relaying** — the same APS payload seen from successive hops infers a path.
   - Link Status frames (routers periodically broadcast neighbor LQI).
2. **Per-link RSSI/LQI** measured by the sniffer for every frame (raw RF truth).
3. **Coordinator view** (optional, [ha-integration.md](ha-integration.md)) — ZHA/Z2M expose a
   network map (neighbor/routing tables). Cross-referencing the two is powerful: a link the
   coordinator believes is fine but that the sniffer sees as marginal is a prime suspect.

## Model

- **Nodes:** devices keyed by extended (IEEE) address, with short address, inferred role
  (coordinator / router / end-device), and friendly name (from HA if integrated).
- **Edges:** directed links with rolling RSSI/LQI stats and last-seen time.
- **Tree:** shortest/observed path from each device to the coordinator; flag multi-hop paths and
  weak links (LQI < 100 yellow, < 50 red — matching Z2M conventions).

## Rendering

Force-directed / hierarchical graph (react-flow or cytoscape in the full UI; a lightweight SVG
force layout in the on-device lite UI). Edge color = LQI band; edge label = hop count / RSSI.
