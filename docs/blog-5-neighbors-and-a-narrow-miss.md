# The Neighbors, the Simulator, and a Narrow Miss on the Schematic

Satellite OTA finally works. That felt like the big remaining risk on the hardware side, so this
stretch has been a mix of catching up on things that got deferred while that fire was burning, plus
one hardware review that turned out to matter more than expected.

## A demo that doesn't lie about itself

Not everyone testing this has a satellite carrier board, three extra radios, and a Hue bridge
sitting around. So: a demo mode, which runs the entire dashboard against a fully simulated
network — a home mesh with a coordinator, routers, and end devices spread across rooms, a Hue
bridge network, a generic foreign Zigbee network, and (see below) a Thread network too — generated
through the exact same decode and decrypt pipeline real hardware uses, not a special-cased "fake
data" branch bolted onto the UI.

The rule I care about more than any of the simulated detail: it should never be possible to mistake
this for a real capture. A visible "DEMO MODE" badge stays on screen the entire time it's active,
and if the host can't find a C6 within a few seconds, it *asks* — retry, or enter demo mode — rather
than quietly switching over on its own. Getting the RF spectrum tab to lie convincingly took a
second pass, too: real Zigbee traffic is bursty, a device transmits for a millisecond and then goes
quiet, so even a channel with an active network should read as noise floor most of the time. The
first version had every "active" channel reading solid hot on every single sweep, which looks
exactly like a jammer, not a network — a good reminder that a convincing simulation has to be
convincingly *boring* most of the time, not maximally dramatic.

## Recognizing the neighbors

Thread — which is what nearly all Matter devices run on top of — shares the exact same 802.15.4
radio and MAC layer as Zigbee. It just uses a completely different network layer above that: 6LoWPAN
and IPv6, instead of Zigbee's own NWK header. Frames from a Thread network were already failing the
Zigbee NWK-version check and falling back gracefully to MAC-only recording — the missing piece was
just noticing *why* they were failing.

A cheap check against known 6LoWPAN framing patterns, added right at that existing fallback point,
now recognizes a Thread network for what it is and labels it "Thread network (likely Matter)"
instead of leaving it an unexplained bare PAN id on the Networks tab. It can't prove the traffic is
specifically Matter without that network's own key — same limit as any other foreign, encrypted
network — but going from "unrecognized" to "honestly labeled" is real progress.

## A schematic almost went to fab wrong

The carrier board design has existed on paper for a while — netlist, pinout table, the works — but
a document isn't hardware, and the real test arrived this week in the form of an actual PADS
schematic export to review before it goes anywhere near a fab house.

Good thing it got a second look. One satellite's module was tagged under a part number for a chip
with no 802.15.4 radio at all — an easy mix-up buried in a component library, and exactly the kind
of thing that's invisible until a board comes back and one radio just doesn't work. A third
satellite was missing one of its two required control lines back to the primary entirely — present
on paper, absent from the actual net. Two satellites had gotten wired directly to each other instead
of each going back to the primary the way they're supposed to. One satellite had no path to power
at all. Every one of those got caught, fixed, and then re-verified pin-by-pin against the actual
firmware GPIO definitions, using the real schematic symbol's pin diagram rather than guessing from
a generic reference. In the process, a leftover reference file in the repo turned out to still
document an *old* pin assignment the firmware had quietly moved away from months ago for good
reason — fixed that too, so it can't mislead the next pass.

None of those were exotic bugs. They were the kind that come from copying one satellite's wiring to
set up the next one and not quite finishing the copy. Which is exactly why a second set of eyes on
the actual netlist — not just the intended design — was worth doing before committing to copper.

That's where things stand right now: software and firmware both proven out, a carrier board design
that's been checked against reality and corrected, and a project that's gone from "an idea about a
cheap radio and a diagnostic dashboard" to something that's actually been run, actually been
updated over the air, and actually had its hardware design caught doing the wrong thing before it
became a permanent mistake in fiberglass.
