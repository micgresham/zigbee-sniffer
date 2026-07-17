# The Week the Sniffer Earned Its Keep

Every project has a moment where it stops being a thing you build and becomes a thing you use.
This week my own Zigbee network fell apart in three different ways, and for the first time the
sniffer wasn't the project — it was the tool I reached for. It found two root causes I would never
have guessed from Home Assistant's side alone, missed a third in a way that taught me something,
and picked up a new dashboard panel as a direct result. Worth writing down while the bruises are
fresh.

## The garage doors that wouldn't come back

It started innocently: after re-pairing every device on the network, my two Tuya garage door
controllers came back half-broken — contact sensor present, control switch gone. The trail led
through custom ZHA quirk files, a deprecated quirks API, a truncated file copy (2,971 bytes where
5,118 should have been — always check the byte count), and finally a genuinely subtle change in
how modern ZHA decides whether to create a switch entity at all: a virtual cluster that answers
"unsupported" for an attribute it never got a value for quietly loses its entity. The device
never reports datapoint 1 on join — it's a stateless trigger — so the fix was seeding a default.

All of that was software, and all of it was fixable. What the sniffer added was the part that
wasn't: its device page for the garage showed **the only path this device has ever had is a direct
link to the coordinator at −96 dBm, with no router to fall back on**, a failed re-interview logged
by zigpy, and a real rejoin event. The quirk was correct by the end. The radio floor it stood on
was not. I ordered different hardware for the garage and stopped fighting physics — but I got to
stop because the dashboard *told me which battle I was actually losing*, instead of letting me
keep polishing a quirk file against a −96 dBm wall.

## Then nothing would pair at all

A day later the whole network went strange: every outbound command failing with `NWK_NO_ROUTE`,
requests queuing thirty seconds deep, pairing dead — while inbound reports kept trickling in like
nothing was wrong. I had theories. Zombie routers from the week's churn. Frame-counter desync.
Each was plausible, each fit some of the log, and each was wrong.

What fixed it was unplugging the Hue bridge.

Here's the part that made it a lesson instead of an anecdote: I assumed channel overlap — Hue
picks from the same ZLL channels my network uses, case closed. Then the sniffer showed the Hue
network sitting on **channel 25**, five channels and 25 MHz away from my network on 20. It had
always been there. The interference wasn't in frequency at all — it was in *space*. The bridge
sat close enough to my coordinator that its transmissions were blocking the coordinator's
receiver regardless of channel (the classic front-end desense every RF person warns about and
every smart-home person forgets, because the bridge lives next to the router, which lives next to
the Home Assistant box, which is where the coordinator is plugged in).

The clue had been on the sniffer's screen the whole time: one Hue device, **199,813 frames at
−54 dBm**. A very loud neighbor, very close to somewhere that mattered. I just didn't have a view
that put "how much air does each network use, and how loudly" in one place — so I found the answer
the dumb way, by unplugging things until the network came back.

## The panel that should have existed already

That stung enough to become a feature the same day. The dashboard now has a **Channel airtime**
panel: frames-per-minute stacked by network over a 15-minute-to-24-hour window, per-channel
totals, and — the part this week proved matters — an **RSSI histogram per network**. High frame
rate plus a distribution piled at the loud end is the "busy transmitter, physically close"
signature, and it reads in about ten seconds. Your network is always blue; the neighbors keep
stable colors (checked against color-blindness simulations, since a chart you can't read is
decoration); the Hue-bridge integration labels its own PAN.

Under the hood it's a per-minute rollup table written at ingest — frames, RSSI sum, and eight
histogram bins per network per channel — because this project already learned the hard way that
one heavy query on the raw packets table can starve ingest and freeze the whole app behind the
database mutex. The view never touches raw packets. Lessons compound if you let them.

## What actually won the week

Tallying it honestly: the sniffer directly diagnosed the garage RF dead zone (topology view,
failed-interview log mining), identified every network in the air including which PAN was Hue's
and what channel it really lived on (the fact that broke my wrong theory), and confirmed the
recovery afterward. It did *not* hand me the desense diagnosis — I had to trip over that by
unplugging the bridge — and that gap is exactly what the airtime panel now covers. That's the
project working the way I hoped it would: the failures it doesn't catch become the features that
catch the next one.

There's also a quiet vindication in the feature list origin story. Foreign-network labeling,
Hue integration, per-device link analysis, incident mining, and now airtime-by-PAN — none of
those came from a roadmap. Every one came from standing in front of a broken network holding a
question the existing tools couldn't answer. The garage, for the record, is getting a
mains-powered router before anything else gets paired out there. Physics doesn't negotiate, but
it does leave fingerprints, and I finally have the tool that dusts for them.
