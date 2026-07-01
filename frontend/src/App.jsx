import React, { useEffect, useState, useCallback } from "react";

// Minimal but functional dashboard wired to the host REST + WebSocket API.
// Richer per-feature pages (Inspector, full routing graph via react-flow,
// spectrum waterfall) build on this scaffold — see frontend/README.md.

const api = (p) => fetch(p).then((r) => r.json());

function useLive() {
  const [status, setStatus] = useState(null);
  const [lastIncident, setLastIncident] = useState(null);
  useEffect(() => {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const ws = new WebSocket(`${proto}//${location.host}/ws`);
    ws.onmessage = (e) => {
      const ev = JSON.parse(e.data);
      if (ev.kind === "status") setStatus(ev);
      if (ev.kind === "incident") setLastIncident(ev);
    };
    return () => ws.close();
  }, []);
  return { status, lastIncident };
}

function lqiClass(lqi) {
  return lqi >= 100 ? "good" : lqi >= 50 ? "fair" : "weak";
}

export default function App() {
  const [devices, setDevices] = useState([]);
  const [routing, setRouting] = useState({ nodes: [], edges: [] });
  const [incidents, setIncidents] = useState([]);
  const [spectrum, setSpectrum] = useState([]);
  const { status, lastIncident } = useLive();

  const refresh = useCallback(() => {
    api("/api/devices").then(setDevices).catch(() => {});
    api("/api/routing").then(setRouting).catch(() => {});
    api("/api/incidents").then(setIncidents).catch(() => {});
    api("/api/spectrum").then(setSpectrum).catch(() => {});
  }, []);

  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 3000);
    return () => clearInterval(t);
  }, [refresh, lastIncident]);

  // latest energy per channel for a simple spectrum view
  const ed = {};
  for (const s of spectrum) ed[s.channel] = s.ed_dbm;

  return (
    <div className="app">
      <header>
        <h1>Zigbee Sniffer &amp; Diagnostics</h1>
        <span className="status">
          {status ? `ch ${status.channel} · ${status.captured} frames · dropped ${status.dropped_buf}` : "waiting for device…"}
        </span>
      </header>

      <div className="grid">
        <section className="panel">
          <h2>Devices ({devices.length})</h2>
          <table>
            <thead><tr><th>Address</th><th>Role</th><th>RSSI</th><th>LQI</th><th>#</th></tr></thead>
            <tbody>
              {devices.map((d) => (
                <tr key={d.addr}>
                  <td>{d.addr}</td><td>{d.role || ""}</td><td>{d.last_rssi}</td>
                  <td className={lqiClass(d.last_lqi)}>{d.last_lqi}</td><td>{d.count}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>

        <section className="panel">
          <h2>RF spectrum</h2>
          <div className="spectrum">
            {Array.from({ length: 16 }, (_, i) => 11 + i).map((c) => {
              const v = ed[c];
              const h = v == null ? 2 : Math.max(2, Math.min(100, (v + 100) * 1.4));
              const cls = v == null ? "dim" : v > -60 ? "weak" : v > -80 ? "fair" : "good";
              return (
                <div className="bar" key={c}>
                  <div className={`fill ${cls}`} style={{ height: `${h}%` }} />
                  <span>{c}</span>
                </div>
              );
            })}
          </div>
        </section>

        <section className="panel">
          <h2>Incidents ({incidents.length})</h2>
          <table>
            <thead><tr><th>When</th><th>Device</th><th>Reason</th><th>Ch</th><th>Energy</th><th>Silent</th></tr></thead>
            <tbody>
              {incidents.map((i) => (
                <tr key={i.id}>
                  <td>{i.ts}s</td><td>{i.addr}</td>
                  <td className={i.reason === "recovered" ? "good" : "weak"}>{i.reason}</td>
                  <td>{i.channel}</td><td>{i.ed}dBm</td><td>{i.silent_s}s</td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>

        <section className="panel">
          <h2>Routing links ({routing.edges.length})</h2>
          <table>
            <thead><tr><th>From</th><th>To</th><th>LQI</th><th>#</th></tr></thead>
            <tbody>
              {routing.edges.map((e, k) => (
                <tr key={k}>
                  <td>{e.src}</td><td>{e.dst}</td>
                  <td className={lqiClass(e.lqi)}>{e.lqi}</td><td>{e.count}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      </div>
    </div>
  );
}
