# Decoding & decryption

> Status: **implemented in the host (Block C)** — `host/zbsniff/decode/` does MAC/NWK/APS/ZCL,
> and `decode/crypto.py` does AES-CCM* decryption with your network key (validated by an
> encrypt→decrypt roundtrip test). Raw capture + pcap done in Block A; the browser lite UI does
> MAC decode today (NWK decryption via WebCrypto is a follow-up).

## What you can see without a key

802.15.4 MAC headers, addresses, frame types, sequence numbers, and per-frame RSSI/LQI/channel.
Enough for link-quality, retry, and timing analysis — but **NWK/APS payloads are encrypted**, so
device identities, clusters, and commands are hidden, and routing reconstruction is limited.

## Decrypting with your network key (recommended)

Zigbee encrypts with **AES-CCM\*** using the **network key** (Trust Center network key). Providing
*your own* key unlocks NWK/APS decode: real device identities, commands, and accurate routing.

### Getting your key from Home Assistant
- **ZHA:** Settings → Devices & Services → ZHA → Configure → download "diagnostics"/backup; the
  network key is in the backup JSON. Or read it from `zigbee.db` / the backup file.
- **Zigbee2MQTT:** `data/coordinator_backup.json`, or `network_key` in `configuration.yaml`
  (often stored as a GenerateKey value after first run).

Treat the key like a password. It is entered:
- in the **host app** config (Block C), or
- on the **device** via `CMD_SET_KEY` (stored in NVS) for on-device/standalone decode, or
- in **Wireshark**: Preferences → Protocols → ZigBee → Pre-configured Keys (key, type "Network").

### Caveats
- **Key rotation** invalidates an old key; some networks rotate periodically.
- Frames encrypted at the **APS layer** with a **link key** (e.g. Trust-Center exchanges,
  touchlink) need that link key too — it isn't derivable from sniffing.
- Decryption is **passive** — we never transmit or join. This is a receiver only.

## Decoder implementation

- **Host (Block C):** `host/zbsniff/decode/` using scapy's `dot15d4`/`zigbee` layers plus custom
  NWK source-route parsing where scapy is thin; `decode/crypto.py` does AES-CCM* via
  `cryptography`.
- **Browser (Block B):** a compact JS decoder for the lite UI; WebCrypto for AES-CCM*.
