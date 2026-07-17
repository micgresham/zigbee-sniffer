# One Radio Hears One Channel

The dashboard is solid now — routing tree, diagnostics, active testing, device naming, all of it
running against a single tethered C6. And that single radio is exactly the limitation I keep
running into.

One radio, one channel, all the time. If I want to know what's happening on channel 15 while I'm
watching channel 20, I have to interrupt my own capture and go look — which means I can't actually
watch both at once, which means the survey that finds a noisy neighbor network is a snapshot, not
a standing watch. The obvious answer is more radios. The interesting question is how to wire them
up without turning this into four separate gadgets that don't talk to each other.

## Satellites, not separate tools

The design that makes sense: keep one primary C6 as the "brain" of the setup — the one talking USB
to the host — and let up to three additional C6 boards act as satellites, each free to sit on its
own channel, each relayed back through the primary over a shared SPI bus instead of needing their
own USB connection. One host, one dashboard, one coherent view — it just happens to be assembled
from up to four radios instead of one.

That means an actual SPI relay layer: the primary as SPI master, each satellite as an SPI slave,
a wire protocol for commands going out and captured frames coming back, in both directions, without
either side blocking the other. And because this now has to be real hardware, not just firmware —
a carrier board: one PCB to host a primary and up to three satellites with clean shared power and a
defined pinout, instead of a nest of loose jumper wires that falls apart if you look at it wrong.

## Deciding who does what

More radios also means a new problem I didn't have with just one: who does what. If a satellite is
already pinned to sniffing channel 20 and I ask for a spectrum sweep, should it just start sweeping
and silently stop hearing anything on channel 20? That's a surprise I don't want to hand anyone.

So radios now get **roles** — sniffer, spectrum, tester, hopper, or just "any" — and requesting a
function walks through an actual allocation decision: use a radio already dedicated to that job if
one exists, otherwise grab a free "any" radio, and if the only candidate is already busy doing
something else, ask before interrupting it rather than just doing it. With one radio that means an
occasional confirmation prompt. With two or more, most things just work without ever asking.

The carrier board design is drawn up, the pin assignments are set, and the firmware side — the SPI
relay, the per-satellite command routing — compiles and runs against real hardware on a bench. Next
is actually getting a satellite to take a firmware update over that link instead of needing to be
plugged into USB by hand every time something changes.
