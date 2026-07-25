# First Light: The Dashboard Finds Its Feet

The plan was: a cheap radio doing the listening, a small host doing the thinking, a dashboard that
tells you what's actually wrong instead of just what happened. This week that plan turned into
something that actually runs.

## The first real build

The tethered C6 is capturing. The Go host is decoding, decrypting, and serving a web dashboard over
it. And — because I wanted the whole shape of the thing working before polishing any one piece —
the first build landed all of it at once: a live routing tree, a live-frames view, an RF spectrum
tab, active device testing, and a first pass at OTA updates so I'm not stuck re-flashing over USB
every time the firmware changes. Zigbee-network discovery came right alongside it: a passive
survey that hops channels listening for traffic, and an active scan that transmits a beacon
request so even quiet, idle networks answer back.

Watching the routing tree build itself for the first time, live, from real captured traffic — that
was the moment this stopped being a plan and started being a thing I actually use.

## Making it survive being left alone

A tool that's only useful while you're staring at it isn't the tool I set out to build. The whole
point is catching a device that goes quiet at 2am while nobody's watching. So the next stretch of
work wasn't new features — it was making the thing trustworthy enough to leave running.

The serial link would occasionally just... go away, and the dashboard would sit there showing stale
data with no indication anything was wrong. That's now fixed: the host notices the silence and
reconnects on its own, no manual intervention. And the actual diagnosis engine landed — automatic
incident detection, watching for a device that used to talk regularly and then didn't, logging
both the silence and the recovery with a timestamp on each. That incident log is really the heart
of the whole project, and now it exists.

Also found and fixed a UI freeze that only showed up after leaving an energy-detect sweep running
for a while — exactly the kind of bug you only catch by actually doing the thing the tool is
supposed to be good for: running unattended, for hours.

## Giving devices an identity

A routing tree full of bare hex addresses is technically correct and practically useless. This
week's other big push was naming things.

Every device's extended address carries a manufacturer OUI — readable even on traffic I can't
decrypt, since it lives in the unencrypted NWK header — so devices now show up labeled by vendor
instead of `0x4a2f`. Pairing a Philips Hue bridge gives the Hue network authoritative naming for
free, no guessing required. Parsing ZDO device-announce messages gives a reliable map from short
address to IEEE address without waiting to get lucky on a join.

On top of that: a frame inspector that shows a layer-by-layer decode of any captured frame, with
pcap export so I can hand a capture to Wireshark when I want a second opinion. Device
interrogation, so clicking a device can show its actual capabilities and a hop-by-hop traceroute
back to the coordinator. None of this was strictly required to answer "did this device go silent"
— but a dashboard that shows you a name, a manufacturer, and a path home is a dashboard you'll
actually leave open.

It's starting to feel less like a packet dump and more like a tool.
