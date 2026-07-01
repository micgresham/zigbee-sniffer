// web-lite app.js — on-device diagnostic UI logic.
//
// The standalone firmware forwards raw captured frames / ED samples / status
// over a WebSocket using the SAME binary framing as the serial protocol
// (/protocol/framing.md). This file ports the framing decoder + payload parsers
// (mirror of host/zbsniff/proto.py) and does decode + aggregation client-side,
// matching the thin-firmware/thick-browser design.
"use strict";

// ---- protocol constants (mirror proto.h / proto.py) -----------------------
const MAGIC0 = 0x5a, MAGIC1 = 0xbe, PROTO_VER = 1;
const MSG = {
  CAPTURED_FRAME: 0x01, ED_RESULT: 0x02, STATUS: 0x03, LOG: 0x04, ACK: 0x05,
  INCIDENT: 0x06,
  CMD_SET_CHANNEL: 0x81, CMD_SET_MODE: 0x82, CMD_START: 0x83, CMD_STOP: 0x84,
  CMD_SET_HOP: 0x85, CMD_ED_SCAN: 0x86, CMD_SET_KEY: 0x87, CMD_GET_STATUS: 0x88,
};
const COORD = "0x0000";   // Zigbee coordinator short address
const CH_MIN = 11, CH_MAX = 26;

// ---- CRC-16/CCITT-FALSE (mirror codec.c) ----------------------------------
function crc16(bytes, start, end) {
  let crc = 0xffff;
  for (let i = start; i < end; i++) {
    crc ^= bytes[i] << 8;
    for (let b = 0; b < 8; b++) {
      crc = (crc & 0x8000) ? ((crc << 1) ^ 0x1021) & 0xffff : (crc << 1) & 0xffff;
    }
  }
  return crc;
}

// ---- streaming frame decoder ----------------------------------------------
class StreamDecoder {
  constructor() { this.buf = new Uint8Array(0); }
  _append(chunk) {
    const n = new Uint8Array(this.buf.length + chunk.length);
    n.set(this.buf, 0); n.set(chunk, this.buf.length); this.buf = n;
  }
  feed(chunk) {
    this._append(chunk);
    const out = [];
    let b = this.buf;
    let i = 0;
    while (true) {
      // find magic
      while (i + 1 < b.length && !(b[i] === MAGIC0 && b[i + 1] === MAGIC1)) i++;
      if (i + 6 > b.length) break;
      const ver = b[i + 2], type = b[i + 3];
      const len = b[i + 4] | (b[i + 5] << 8);
      if (ver !== PROTO_VER || len > 2048) { i++; continue; }
      const total = 6 + len + 2;
      if (i + total > b.length) break;
      const crcRx = b[i + 6 + len] | (b[i + 7 + len] << 8);
      if (crc16(b, i + 2, i + 6 + len) === crcRx) {
        out.push({ type, payload: b.subarray(i + 6, i + 6 + len) });
        i += total;
      } else {
        i++;
      }
    }
    this.buf = b.subarray(i);
    return out;
  }
}

// ---- payload parsers ------------------------------------------------------
function u64le(p, o) {
  let v = 0n;
  for (let i = 0; i < 8; i++) v |= BigInt(p[o + i]) << BigInt(8 * i);
  return v;
}
function i8(v) { return v > 127 ? v - 256 : v; }

function parseCaptured(p) {
  return {
    radio_id: p[0], channel: p[1], rssi: i8(p[2]), lqi: p[3], flags: p[4],
    ts: u64le(p, 5),
    mpdu: p.subarray(14, 14 + p[13]),
  };
}
function parseEd(p) {
  return { radio_id: p[0], channel: p[1], ed_dbm: i8(p[10]),
           sweep_id: p[11] | (p[12] << 8) };
}
function parseStatus(p) {
  const dv = new DataView(p.buffer, p.byteOffset, p.byteLength);
  return {
    radio_id: p[0], mode: p[1], channel: p[2],
    hop_mask: dv.getUint32(3, true), hop_dwell_ms: dv.getUint16(7, true),
    uptime_s: dv.getUint32(9, true), captured: dv.getUint32(13, true),
    dropped_crc: dv.getUint32(17, true), dropped_buf: dv.getUint32(21, true),
    fw_major: p[25], fw_minor: p[26],
  };
}

// ---- minimal IEEE 802.15.4 MAC decode -------------------------------------
// Enough for diagnostics: frame type + addressing. NWK/APS decode (and optional
// AES-CCM* via WebCrypto) come later.
const FT = ["Beacon", "Data", "Ack", "MAC Cmd", "?", "?", "?", "?"];
function hex16(v) { return "0x" + v.toString(16).padStart(4, "0"); }
function decodeMac(mpdu) {
  if (mpdu.length < 3) return { type: "?", summary: "(runt)" };
  const fcf = mpdu[0] | (mpdu[1] << 8);
  const ftype = fcf & 0x7;
  const panComp = (fcf >> 6) & 1;
  const destMode = (fcf >> 10) & 0x3;
  const srcMode = (fcf >> 14) & 0x3;
  let o = 3; // FCF(2) + seq(1)
  const r = { type: FT[ftype], seq: mpdu[2], dst: null, src: null, dstPan: null };
  const rd16 = () => { const v = mpdu[o] | (mpdu[o + 1] << 8); o += 2; return v; };
  const rd64 = () => { let s = "0x"; for (let k = 7; k >= 0; k--) s += mpdu[o + k].toString(16).padStart(2, "0"); o += 8; return s; };
  if (destMode) { r.dstPan = rd16(); r.dst = destMode === 2 ? hex16(rd16()) : rd64(); }
  if (srcMode) { if (!panComp) rd16(); r.src = srcMode === 2 ? hex16(rd16()) : rd64(); }
  r.summary = `${r.type} seq=${r.seq}` + (r.src ? ` ${r.src}→${r.dst ?? "?"}` : "");
  return r;
}

// ---- aggregation state ----------------------------------------------------
const devices = new Map();     // addr -> {addr, lastSeen, count, rssi, lqi}
const edges = new Map();       // "src->dst" -> {src, dst, lqi, count}
const edByChannel = new Map(); // channel -> dBm
let frameLog = [];
let incidents = [];            // {ts, addr, reason, rssi, lqi, ch, ed, silent_s}
let lastStatus = null;
let totalFrames = 0;

function noteDevice(addr, f) {
  if (!addr) return;
  let d = devices.get(addr);
  if (!d) { d = { addr, count: 0 }; devices.set(addr, d); }
  d.count++; d.lastSeen = Date.now(); d.rssi = f.rssi; d.lqi = f.lqi; d.channel = f.channel;
}

function noteEdge(src, dst, lqi) {
  if (!src || !dst || dst === "0xffff") return;
  const key = src + "->" + dst;
  let e = edges.get(key);
  if (!e) { e = { src, dst, count: 0 }; edges.set(key, e); }
  e.count++; e.lqi = lqi;
}

// ---- WebSocket ------------------------------------------------------------
let ws = null;
const dec = new StreamDecoder();

function wsUrl() {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const host = location.host || "192.168.4.1";
  return `${proto}//${host}/ws`;
}

function connect() {
  ws = new WebSocket(wsUrl());
  ws.binaryType = "arraybuffer";
  ws.onopen = () => { setConn(true); fetchIncidents(); };
  ws.onclose = () => { setConn(false); setTimeout(connect, 1500); };
  ws.onerror = () => ws.close();
  ws.onmessage = (ev) => {
    const chunk = new Uint8Array(ev.data);
    for (const m of dec.feed(chunk)) handleMessage(m);
  };
}

function handleMessage(m) {
  if (m.type === MSG.CAPTURED_FRAME) {
    const f = parseCaptured(m.payload);
    const mac = decodeMac(f.mpdu);
    noteDevice(mac.src, f);
    noteEdge(mac.src, mac.dst, f.lqi);
    totalFrames++;
    frameLog.unshift({ ...f, mac });
    if (frameLog.length > 200) frameLog.pop();
  } else if (m.type === MSG.ED_RESULT) {
    const e = parseEd(m.payload);
    edByChannel.set(e.channel, e.ed_dbm);
  } else if (m.type === MSG.STATUS) {
    lastStatus = parseStatus(m.payload);
  } else if (m.type === MSG.INCIDENT) {
    try { incidents.unshift(JSON.parse(new TextDecoder().decode(m.payload))); } catch (e) {}
    if (incidents.length > 200) incidents.pop();
  }
}

// Pull the persisted incident log (JSON lines) from the device on connect.
function fetchIncidents() {
  fetch("/incidents").then(r => r.text()).then(t => {
    const seen = new Set(incidents.map(i => i.ts + i.addr + i.reason));
    t.split("\n").filter(Boolean).forEach(line => {
      try {
        const inc = JSON.parse(line);
        const k = inc.ts + inc.addr + inc.reason;
        if (!seen.has(k)) { incidents.push(inc); seen.add(k); }
      } catch (e) {}
    });
    incidents.sort((a, b) => b.ts - a.ts);
  }).catch(() => {});
}

// ---- commands -------------------------------------------------------------
function encode(type, payload = new Uint8Array(0)) {
  const body = new Uint8Array(4 + payload.length);
  body[0] = PROTO_VER; body[1] = type;
  body[2] = payload.length & 0xff; body[3] = (payload.length >> 8) & 0xff;
  body.set(payload, 4);
  const crc = crc16(body, 0, body.length);
  const out = new Uint8Array(2 + body.length + 2);
  out[0] = MAGIC0; out[1] = MAGIC1; out.set(body, 2);
  out[out.length - 2] = crc & 0xff; out[out.length - 1] = (crc >> 8) & 0xff;
  return out;
}
function send(type, payload) {
  if (ws && ws.readyState === WebSocket.OPEN) ws.send(encode(type, payload));
}
function setChannel(ch) { send(MSG.CMD_SET_CHANNEL, new Uint8Array([ch])); }
function setMode(mode) { send(MSG.CMD_SET_MODE, new Uint8Array([mode])); }
function edScan() {
  let mask = 0; for (let c = CH_MIN; c <= CH_MAX; c++) mask |= 1 << (c - CH_MIN);
  const p = new Uint8Array(6);
  new DataView(p.buffer).setUint32(0, mask, true);
  new DataView(p.buffer).setUint16(4, 5, true);
  send(MSG.CMD_ED_SCAN, p);
}

// ---- rendering ------------------------------------------------------------
function setConn(ok) {
  const el = document.getElementById("conn");
  el.textContent = ok ? "connected" : "disconnected";
  el.className = ok ? "ok" : "bad";
}

function fmtAge(ts) {
  const s = Math.floor((Date.now() - ts) / 1000);
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m`;
}
function lqiClass(lqi) { return lqi >= 100 ? "good" : lqi >= 50 ? "fair" : "weak"; }

function render() {
  // status bar
  if (lastStatus) {
    document.getElementById("status").innerHTML =
      `ch <b>${lastStatus.channel}</b> · mode ${lastStatus.mode} · ` +
      `frames <b>${lastStatus.captured}</b> · dropped ${lastStatus.dropped_buf} · ` +
      `up ${lastStatus.uptime_s}s · fw ${lastStatus.fw_major}.${lastStatus.fw_minor} · ` +
      `seen ${totalFrames}`;
  }
  // devices
  const devs = [...devices.values()].sort((a, b) => b.lastSeen - a.lastSeen);
  document.getElementById("devices").innerHTML = devs.map(d =>
    `<tr><td>${d.addr}</td><td>${d.channel ?? ""}</td>` +
    `<td>${d.rssi ?? ""}</td><td class="${lqiClass(d.lqi)}">${d.lqi ?? ""}</td>` +
    `<td>${d.count}</td><td>${fmtAge(d.lastSeen)}</td></tr>`).join("") ||
    `<tr><td colspan="6" class="dim">no devices yet — is the channel right?</td></tr>`;
  // frame log
  document.getElementById("frames").innerHTML = frameLog.slice(0, 60).map(f =>
    `<tr><td>${f.channel}</td><td>${f.rssi}</td><td>${f.lqi}</td>` +
    `<td>${f.mac.type}</td><td>${f.mac.summary}</td></tr>`).join("");
  // spectrum
  const spec = document.getElementById("spectrum");
  let bars = "";
  for (let c = CH_MIN; c <= CH_MAX; c++) {
    const dbm = edByChannel.get(c);
    const h = dbm == null ? 0 : Math.max(2, Math.min(100, (dbm + 100) * 1.4));
    const cls = dbm == null ? "dim" : dbm > -60 ? "weak" : dbm > -80 ? "fair" : "good";
    bars += `<div class="bar"><div class="fill ${cls}" style="height:${h}%"></div>` +
            `<span>${c}</span></div>`;
  }
  spec.innerHTML = bars;

  renderRouting();
  renderIncidents();
}

// Radial routing tree: BFS hop-distance from the coordinator (0x0000), nodes
// placed on rings by hop count, edges colored by LQI. Reconstructed from
// observed MAC src→dst links (raw RF truth); see docs/routing.md.
function renderRouting() {
  const svg = document.getElementById("routing");
  const W = svg.clientWidth || 460, H = 300, cx = W / 2, cy = H / 2;

  // adjacency (undirected) + node set
  const adj = new Map();
  const nodes = new Set();
  const addAdj = (a, b) => { if (!adj.has(a)) adj.set(a, new Set()); adj.get(a).add(b); };
  for (const e of edges.values()) {
    nodes.add(e.src); nodes.add(e.dst); addAdj(e.src, e.dst); addAdj(e.dst, e.src);
  }
  for (const a of devices.keys()) nodes.add(a);
  if (nodes.size === 0) { svg.innerHTML = ""; return; }
  nodes.add(COORD);

  // BFS hop levels from coordinator
  const level = new Map([[COORD, 0]]);
  let frontier = [COORD];
  while (frontier.length) {
    const next = [];
    for (const n of frontier) for (const m of (adj.get(n) || [])) {
      if (!level.has(m)) { level.set(m, level.get(n) + 1); next.push(m); }
    }
    frontier = next;
  }
  let maxL = 0;
  for (const a of nodes) { if (!level.has(a)) level.set(a, -1); maxL = Math.max(maxL, level.get(a)); }
  const isoL = maxL + 1;  // unreachable nodes ring

  // assign ring positions
  const byLevel = new Map();
  for (const a of nodes) {
    const l = level.get(a) < 0 ? isoL : level.get(a);
    if (!byLevel.has(l)) byLevel.set(l, []);
    byLevel.get(l).push(a);
  }
  const pos = new Map();
  const ringR = Math.min(cx, cy) / (isoL + 0.5);
  for (const [l, arr] of byLevel) {
    arr.sort();
    arr.forEach((a, i) => {
      if (l === 0) { pos.set(a, [cx, cy]); return; }
      const ang = (i / arr.length) * Math.PI * 2 - Math.PI / 2;
      pos.set(a, [cx + Math.cos(ang) * ringR * l, cy + Math.sin(ang) * ringR * l]);
    });
  }

  const lqiColor = (lqi) => lqi == null ? "#3a4452" : lqi >= 100 ? "#3fb950" : lqi >= 50 ? "#d29922" : "#f85149";
  let s = "";
  for (const e of edges.values()) {
    const a = pos.get(e.src), b = pos.get(e.dst);
    if (!a || !b) continue;
    s += `<line x1="${a[0].toFixed(1)}" y1="${a[1].toFixed(1)}" x2="${b[0].toFixed(1)}" y2="${b[1].toFixed(1)}" stroke="${lqiColor(e.lqi)}" stroke-width="1.5" opacity="0.7"/>`;
  }
  for (const [a, p] of pos) {
    const isCoord = a === COORD;
    const r = isCoord ? 7 : 4;
    const fill = isCoord ? "#58a6ff" : level.get(a) < 0 ? "#f85149" : "#c9d4e0";
    s += `<circle cx="${p[0].toFixed(1)}" cy="${p[1].toFixed(1)}" r="${r}" fill="${fill}"/>`;
    s += `<text x="${p[0].toFixed(1)}" y="${(p[1] - 7).toFixed(1)}" font-size="9" fill="#7b8794" text-anchor="middle">${isCoord ? "coord" : a}</text>`;
  }
  svg.innerHTML = s;
}

function renderIncidents() {
  const el = document.getElementById("incidents");
  if (!incidents.length) {
    el.innerHTML = `<tr><td colspan="6" class="dim">no incidents logged</td></tr>`;
    return;
  }
  el.innerHTML = incidents.slice(0, 50).map(i => {
    const cls = i.reason === "recovered" ? "good" : "weak";
    return `<tr><td>${i.ts}s</td><td>${i.addr}</td><td class="${cls}">${i.reason}</td>` +
      `<td>${i.rssi ?? ""}/${i.lqi ?? ""}</td><td>${i.ed != null ? i.ed + "dBm" : ""}</td>` +
      `<td>${i.silent_s != null ? i.silent_s + "s" : ""}</td></tr>`;
  }).join("");
}

// ---- boot -----------------------------------------------------------------
function boot() {
  document.getElementById("chSet").addEventListener("click", () => {
    const v = parseInt(document.getElementById("chInput").value, 10);
    if (v >= CH_MIN && v <= CH_MAX) setChannel(v);
  });
  document.getElementById("edBtn").addEventListener("click", edScan);
  document.getElementById("capBtn").addEventListener("click", () => setMode(1));
  connect();
  setInterval(render, 500);
}

if (typeof document !== "undefined") {
  document.addEventListener("DOMContentLoaded", boot);
}

// Exported for unit testing under node.
if (typeof module !== "undefined") {
  module.exports = { crc16, StreamDecoder, parseCaptured, parseStatus, decodeMac, encode, MSG };
}
