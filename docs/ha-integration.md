# Home Assistant integration (optional)

> Status: **implemented (Block C)** — `host/zbsniff/integrations/` (Z2M over MQTT, ZHA over the HA
> WebSocket API) feeding a name `DeviceRegistry` that the API uses to resolve sniffed addresses to
> friendly names. Parsing is unit-tested; live connections need your Z2M/HA to validate. Entirely
> optional and configurable — the tool works standalone without it. Enable via
> `--z2m-host` / `--zha-url --zha-token` (or the `ZB_Z2M_HOST` / `ZB_ZHA_URL` / `ZB_ZHA_TOKEN`
> env vars, also exposed as HA add-on options).

Cross-referencing the sniffer's **raw RF truth** with the coordinator's **own view** is the most
powerful diagnostic: it resolves sniffed addresses to friendly names and lets you confirm that HA
*also* saw a device drop (and when it recovered).

## Zigbee2MQTT (MQTT)

- Subscribe to `zigbee2mqtt/bridge/devices` for the device list (addresses, names, type).
- Request a network map: publish to `zigbee2mqtt/bridge/request/networkmap` (raw/graphviz),
  receive routes + LQI on the response topic.
- Subscribe to device availability topics to timestamp drops/recoveries.
- Config: MQTT host/port/credentials + base topic.

## ZHA (Home Assistant WebSocket API)

- Connect to HA's WebSocket API with a **long-lived access token**.
- Pull the device registry and ZHA topology (neighbors/routes, LQI/RSSI as the coordinator sees
  them).
- Subscribe to state changes for availability.
- Config: HA base URL + long-lived token.

## Philips Hue bridge (name Hue devices & networks)

A Hue bridge is its **own Zigbee coordinator** on its own PAN/channel, separate from your ZHA
network. Its local API exposes each device's **name + extended (IEEE) MAC** and the bridge's
**Zigbee channel**, which the host feeds into the name registry **by extended address** (globally
unique, so it never collides with your ZHA network's short addresses). HA/ZHA and Hue run **at the
same time** — both feed one registry.

- **Pairing (link button):** enter the bridge IP in **Config → Philips Hue bridge**, press the
  round link button on the bridge, then click **Pair**. The host creates an application key via the
  bridge's `POST /api` and stores it **encrypted at rest** (`hue_key` in `zbsniff.yaml`, like the HA
  token). Endpoints: `POST /api/hue_pair?host=`, `GET /api/hue_status`, `POST /api/hue_forget`.
- **What it names:** the **Hue network itself** — the bridge reports its Zigbee channel, so a
  foreign network on that channel is labeled "Philips Hue (bridge)" in the *Zigbee networks* panel
  regardless of what we've heard. Individual **Hue device** names only appear when the sniffer
  actually hears those devices, which needs capture on the Hue channel (a Hue bridge is usually on a
  *different* channel than your ZHA network, and one radio hears one channel).
- Implementation: [`tether/internal/names/hue.go`](../tether/internal/names/hue.go).

## Network identity by manufacturer (OUI) — no setup

Every extended address begins with a 24-bit **OUI** identifying the silicon vendor. When the
sniffer hears an extended address on a foreign PAN, it labels that network by manufacturer —
**Philips Hue** (`00:17:88`), **Aqara/Xiaomi**, **IKEA/Silicon Labs**, **Texas Instruments**, etc.
— with zero configuration; the Hue integration then layers real *device* names on top. The vendor
table lives in [`tether/internal/names/oui.go`](../tether/internal/names/oui.go); add rows freely.
See [networks.md](networks.md).

## What the correlation gives you

- **Name resolution:** sniffed short/extended addresses → friendly names in every view.
- **Map comparison:** overlay the coordinator's routing map on the sniffer's observed links;
  flag links the coordinator trusts but the sniffer sees as marginal.
- **Incident confirmation:** each [incident](incidents.md) shows the coordinator's availability
  timeline for that device alongside the RF context.

## Packaging

When run as the **HA add-on** ([deployment-addon.md](deployment-addon.md)), the add-on can read
HA's API directly and discover the Z2M/ZHA setup with minimal configuration.
