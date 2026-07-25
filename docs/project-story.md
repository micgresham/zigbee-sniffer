# The Zigbee Sniffer story

## Why this exists

Every smart home eventually gets the same bug: a device goes quietly unresponsive. Home Assistant
says "unavailable." Power-cycling it fixes things, for a while. Was it the device? The mesh? The
coordinator? Interference from the neighbor's Wi-Fi, or their Zigbee, or — increasingly — their
Thread border router? There's no way to know, because Zigbee failures are invisible by design:
the protocol has no equivalent of "check engine light." The only ground truth is the radio traffic
itself, and getting at that meant either paying for a commercial sniffer dongle or learning
Wireshark's 802.15.4 dissector well enough to read raw frames by hand.

The idea was simple: an ESP32-C6 already has the radio. Point it at the air, decode what comes
back, and build the diagnostics layer that Zigbee itself never gave you — not just "here are the
packets," but "here's what's actually wrong, ranked by how much you should care." That's the whole
project: a sniffer that does the diagnosis, not just the capturing.

## Building the diagnostic core

The first real milestone landed all the primary pieces at once: a tethered C6 doing the listening,
a Go host doing the decode/decrypt/serve, and a web dashboard with a routing tree, live frames, an
RF spectrum view, active testing, and a first pass at OTA updates. Zigbee-network discovery came
right alongside it — a passive survey plus an active beacon-request scan, so you could see what
else was on the air, not just your own mesh.

The next stretch was about making it survive contact with reality. A serial link that quietly drops
is worse than one that never connects — so the host learned to self-heal, reconnecting on its own
instead of leaving you staring at a stale dashboard. Automatic incident detection came in: silence
and recovery events, timestamped, which is really the whole point of the tool distilled into one
list. A UI freeze in the energy-detect sweep mode got found and fixed, the kind of bug you only
catch by leaving the thing running for hours, which is exactly how you're supposed to use it.

Then came the identity work, and this is where the dashboard stopped feeling like a packet dump and
started feeling like a tool. Every device's extended address carries a manufacturer OUI, readable
even on encrypted traffic because it lives in the unencrypted NWK header — so devices started
showing up labeled by vendor instead of bare hex. Pairing a Philips Hue bridge gave authoritative
network naming for free. ZDO device-announce parsing meant a reliable short-address-to-IEEE map
without waiting to get lucky on a join. A frame inspector with pcap export meant you could hand a
capture to Wireshark when you needed a second opinion. Device interrogation added a real
coordinator-to-device traceroute. None of this was strictly necessary for "detect that a device
went silent" — but a tool that tells you a device's name, manufacturer, and hop count is a tool
you'll actually leave running.

## Wanting more ears: going multi-radio

One radio hears one channel. That's fine for watching your own network, but it means every
question about "what else is out there" requires interrupting your own capture to go look. The
answer was satellites: additional ESP32-C6 boards, each free to sit on its own channel, relayed
back through the primary over a shared SPI bus instead of needing their own USB connections. That
meant designing an actual carrier board — a real piece of hardware, not just firmware — to host a
primary and up to three satellites on one board with clean shared power and a defined SPI/CS/DATA_READY
pinout.

Getting the firmware side working came first: an SPI relay layer on the primary, a corresponding
SPI-slave transport on each satellite, and a wire protocol for commands and captured frames to flow
both directions. Multi-radio also meant rethinking allocation — with more than one radio available,
the host needed to decide which radio does what (sniff, sweep, probe, hop) without the user having
to micromanage it, dedicating radios by role and asking for confirmation only when something would
actually have to be interrupted.

## The OTA saga

Updating a satellite's firmware without unplugging it — over the SPI link it's already using to
relay captures — turned into the hardest bug of the whole project, because it was a genuine
chicken-and-egg problem: the fix for corrupted OTA transfers was itself a firmware update, and that
update needed an *uncorrupted* transfer to arrive in order to install.

The root cause took a while to isolate. The satellite's OTA write path had no way to tell a
**resent** chunk — the completely normal result of a lost acknowledgment on a lossy SPI link — from
genuinely new data, so a retry could silently double-write a chunk and shift everything after it,
corrupting the image with no visible error until the very end. A separate, unrelated bug was
tripping the chip's own watchdog: erasing the whole flash partition in one long blocking call was
just slow enough, on this single-core chip, to starve the idle task the watchdog was monitoring.

Both got fixed properly: the satellite now writes directly to the raw flash partition instead of
going through the stateful `esp_ota_write()` API, tracks exactly how many bytes it's already
applied, and skips — rather than re-writes — any chunk whose offset it's already seen. Flash erasing
happens incrementally, one sector at a time as data arrives, instead of one long upfront blocking
call. But getting that fix *onto* a satellite that had never successfully completed an OTA cycle
meant one manual USB reflash, by hand — which had its own detour, since the standard BOOT+RESET
button sequence drops these chips into their **native** USB download mode, a completely different
electrical path than the external UART adapter that had been used for console logs all along.

With the fix actually running on hardware, the real path — OTA over the SPI carrier link — got
proven twice: a firmware push from v0.29 to v0.30, and another from v0.30 to v0.31, both needing
constant chunk retries the whole way through, neither producing so much as a single bad byte.

## Seeing what you can't afford to buy: demo mode

Not everyone has a satellite carrier board, three SuperMini boards, and a Hue bridge sitting around
to try this on. Demo mode runs the entire dashboard against a fully simulated network — a
thirteen-device home mesh, a Hue bridge network, a foreign Zigbee network, and (once Thread/Matter
detection existed) a Thread network too — generated through the exact same decode and decrypt
pipeline real hardware uses, rather than a special-cased UI mode bolted on the side.

The one rule that mattered more than any of the simulated detail: never let fake data pass as real.
A visible "DEMO MODE" badge stays on screen the entire time, and if the host can't find a C6 within
a few seconds, it asks — Retry, or Enter Demo Mode — instead of silently switching over. The
simulated RF spectrum tab needed its own honesty pass too: real Zigbee traffic is bursty, transmitting
for a millisecond and then going quiet, so even a channel with an active network should read as
noise floor most of the time. The first version made every active channel read solid-hot on every
single sweep, which looks exactly like a jammer, not a network.

## The neighbors' new protocol: Thread and Matter

Thread — which is what nearly all Matter devices run over — shares the exact same 802.15.4 MAC
layer as Zigbee, but uses a completely different network layer above it: 6LoWPAN/IPv6 instead of
Zigbee's own NWK header. Frames from a Thread network were already failing the decoder's Zigbee
NWK version check and degrading gracefully to MAC-only recording — the missing piece was noticing
*why* they failed. A cheap dispatch-byte check, slotted into that exact existing fallback point,
recognizes the standard 6LoWPAN framing patterns and labels that network "Thread network (likely
Matter)" instead of leaving it a bare, unexplained PAN id. It can't prove Matter specifically
without that network's own key — the same limit as any other foreign, encrypted network — but it's
an honest, useful label where there used to be none.

## From schematic to silicon: verifying the carrier board

The carrier board had been a documented design for a while — a netlist, a pinout table, a written
GPIO map — but a document isn't hardware. The real test came when an actual PADS/EasyEDA schematic
export showed up for review, and cross-checking it caught exactly the kind of mistakes that are
easy to make and expensive to discover after fabrication: one satellite's module was mislabeled
under a part number for a chip with no 802.15.4 radio at all; a third satellite was missing one of
its two required control lines back to the primary entirely; two satellites had been wired directly
to each other instead of each going back to the primary; one satellite had no path to power. Every
one of those got fixed and re-verified, pin-by-pin, against the firmware's actual GPIO
definitions — and in the process, a stale reference document (`hardware/carrier/netlist.csv`, still
specifying pins that don't exist on the real boards) got caught and corrected too, so the next
person reading it isn't misled by a design that was already superseded.

## Where it stands

The core sniffer-and-diagnostics loop has been solid since early on. Multi-radio satellites work,
OTA-update them over SPI, survive a genuinely lossy link without corruption, and can now be
demonstrated on a fully verified carrier board design. Thread and Matter networks get correctly
identified alongside Zigbee and Hue. Anyone can try the whole thing with zero hardware via demo
mode. What's left is mostly building it out further: fabricating and assembling the actual carrier
board from the now-verified schematic, validating the tethered board's own self-update path on
hardware the same way satellite OTA already has been, and, as always, more field time — because the
entire reason this exists is to catch the failure that only shows up after the tool has been
watching for a while.

That field time has started paying off for real: in one week of debugging my own network the
sniffer diagnosed a garage RF dead zone, unmasked a Hue bridge desensing the coordinator from a
*different* channel, and gained a per-network channel-airtime panel (frames/min by PAN + RSSI
histograms) built directly from the question that week left unanswered — the full story is in
[blog 6](blog-6-the-week-the-sniffer-earned-it.md).

The carrier board itself has since come back from the fab, populated, and worked on the first
power-up — the schematic review paid for itself, save for one reversed selector pair caught in
the first ten minutes of bring-up and fixed in firmware rather than a respin. Photos and the full
story are in [blog 7](blog-7-the-board-comes-home.md). The only work left on that front is no
longer electrical: an enclosure, and a 3D printer.
