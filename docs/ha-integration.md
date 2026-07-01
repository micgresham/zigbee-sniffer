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

## What the correlation gives you

- **Name resolution:** sniffed short/extended addresses → friendly names in every view.
- **Map comparison:** overlay the coordinator's routing map on the sniffer's observed links;
  flag links the coordinator trusts but the sniffer sees as marginal.
- **Incident confirmation:** each [incident](incidents.md) shows the coordinator's availability
  timeline for that device alongside the RF context.

## Packaging

When run as the **HA add-on** ([deployment-addon.md](deployment-addon.md)), the add-on can read
HA's API directly and discover the Z2M/ZHA setup with minimal configuration.
