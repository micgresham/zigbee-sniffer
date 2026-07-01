# web-lite — on-device diagnostic UI (standalone build)

Self-contained single-page UI embedded into the `standalone` firmware and served over WiFi. Per
the [thin-firmware/thick-browser design](../../docs/architecture.md), the firmware forwards raw
captured frames / ED samples / status over a WebSocket using the **same binary wire framing** as
the serial protocol ([../../protocol/framing.md](../../protocol/framing.md)); this UI does the
decode, device aggregation, spectrum, and (later) routing/incident analysis client-side. So
`src/app.js` contains a JS port of the framing decoder + payload parsers, mirroring
`host/zbsniff/proto.py` and `firmware/src/core/codec.c`.

## Transport

- WebSocket at `ws://<device>/ws`, `binaryType = "arraybuffer"`.
- Device → browser: binary `CAPTURED_FRAME`, `ED_RESULT`, `STATUS` frames.
- Browser → device: binary command frames (`CMD_SET_CHANNEL`, `CMD_SET_MODE`, `CMD_ED_SCAN`, ...).

## Build

```bash
cd firmware/web-lite
./build.sh
```
This inlines `styles.css` + `app.js` into a single `dist/index.html`, gzips it, and emits
`../src/standalone/web_assets/web_assets.c` — a plain C byte array (`index_html_gz[]` +
`index_html_gz_len`) compiled directly into the firmware by `src/CMakeLists.txt`. No bundler or
embed-file magic required. Re-run after changing the UI, then rebuild the `standalone` firmware.

## Status

MVP (Block B): status bar, live device list, recent-frames log with MAC decode, ED spectrum bars,
channel control. Routing tree, incident view, and NWK decryption (WebCrypto AES-CCM*) follow.
