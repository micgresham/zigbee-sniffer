# RF spectrum & interference analysis

> Status: ED primitive implemented in firmware (`core/ed_scan.c`) and surfaced via `ED_RESULT`.
> The waterfall UI + channel advisor land in Block B (on-device) and Block C (host).

## How it works

The 802.15.4 radio can measure **energy on a channel** without decoding anything
(`esp_ieee802154_energy_detect()`). Sweeping channels 11–26 and sampling each repeatedly builds
a **noise-floor profile** over time — this is our spectrum analyzer. It does not identify the
*source* directly (it's energy, not a demodulated signal), but combined with the
[Wi-Fi overlap table](channels-reference.md) it strongly implicates sources.

## What you get

- **Per-channel noise floor** (dBm) and a **waterfall** (channel × time × energy heatmap).
- **Wi-Fi overlap overlay** — map raised energy to the Wi-Fi channels that sit on it.
- **Channel advisor** — recommend the quietest Zigbee channel clear of your busy Wi-Fi channels
  (typically 15 / 20 / 25), with a caution that *moving* a Zigbee network forces all devices to
  re-join.
- **Correlation with incidents** — capture the channel energy at the moment a device dropped
  (see [incidents.md](incidents.md)) to test the "interference caused the drop" hypothesis.

## Single-radio compromise vs. satellites

One radio can either capture **or** measure energy at any instant. The `CAPTURE_PLUS_ED` mode
interleaves them (you miss some frames during ED). A **second radio** removes the tradeoff: pin
radio A to the HA channel for continuous capture while radio B sweeps. See
[multi-radio.md](multi-radio.md).

## Interpreting values

- ED is approximate and board-dependent; treat it as **relative**, not calibrated dBm.
- A channel that is quiet at the sniffer's location may be noisy at a device's location — survey
  near the problem device, or use a satellite placed there.

## Energy is not the whole story — see the networks too

ED tells you a channel is *busy*, not *who* is on it. The **Zigbee networks** panel on the same
tab identifies the actual networks (by PAN id) sharing the band, and a survey/active scan finds
those on other channels. A raised noise floor **plus** a foreign PAN on your channel is a strong
interference signal. See [networks.md](networks.md).

The **Channel airtime** panel closes the loop with decoded-frame evidence: frames/min stacked by
PAN plus a per-PAN RSSI histogram, over up to 24 h. It catches the case ED and channel-lists both
miss — a busy network on a *different* channel that is physically close enough (loud RSSI) to
desense your coordinator's receiver. Details in [networks.md](networks.md).
