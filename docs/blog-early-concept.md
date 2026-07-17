# The Case of the Vanishing Smart Bulb

There's a bug every smart home eventually gets, and it's always the same shape: a device just...
stops. Home Assistant shows it "unavailable." I power-cycle it, and it comes back like nothing
happened. A week later, it does it again. Is it the device? The mesh? Something on the same radio
channel drowning it out? There's no way to know — because Zigbee, for all it gets right, gives you
absolutely no visibility into why a device went quiet. No check-engine light. No log. Just
silence, and a guess.

I'm tired of guessing.

## The gap nobody's filling

The "real" way to see what's happening on a Zigbee network is to sniff the actual radio traffic —
but that means either buying a purpose-built sniffer dongle or learning to read raw 802.15.4
frames in Wireshark by hand. Neither of those is something a person troubleshooting their porch
light should have to do. The tools that exist are built for protocol engineers, not for "why did
my door sensor go offline at 2am."

That gap is the whole reason I'm starting this project: not just a sniffer, but something that
looks at the traffic and tells you, in plain terms, what's actually wrong.

## The idea

Here's the thing that made it click: an ESP32-C6 already *has* the radio this needs. It's a
mainstream, cheap microcontroller with a native 802.15.4 radio built in — the same radio family
Zigbee itself runs on. Point one at the air, decode what comes back, and I've got a sniffer for
the price of a dev board instead of a specialty tool.

But a raw packet capture isn't the goal. The goal is a device that watches the network the way an
engineer would if they had nothing else to do all day: noticing when something that used to talk
regularly goes silent, noticing when a neighbor's network shows up on my channel, noticing when
the air itself is just noisy. Capture is the input. Diagnosis is the point.

## The plan

A few decisions are shaping this before a single line of firmware exists:

**Split the brain from the ears.** The C6 is small — plenty of headroom to listen to the radio,
not enough to also decrypt, store, and analyze a running history of thousands of frames while
serving a web dashboard. So the design splits the job in two: the microcontroller does nothing but
capture and hand frames off, and a small host program — decoding, decrypting, storing, and serving
the actual dashboard — does the thinking. The radio stays cheap and disposable; the intelligence
lives somewhere with real CPU and RAM to spare.

**Show the mesh, not just the packets.** A list of frames scrolling past tells you traffic exists.
It doesn't tell you *how the network is shaped* — which devices route through which, and how far
a signal has to travel to get home. So a live routing tree is part of the plan from day one: watch
the mesh build itself in real time, not just read a log about it.

**Separate "the device is broken" from "the radio channel is a mess."** One of the most common
false alarms in any wireless troubleshooting is blaming the device for what's actually
interference. So the design includes an RF view of the spectrum itself from the start — because
"is channel 15 just noisy right now" is a question I need to be able to answer *before* I start
suspecting the hardware.

**Findings, not just facts.** Anyone can dump a table of every frame captured. Far fewer tools tell
you which three things in that table are actually worth worrying about. The plan is automatic
diagnostics from the outset: rank what the data shows by how much it should matter, instead of
handing over a firehose and calling it a feature.

**Listen first, act only on purpose.** The default posture is entirely passive — it never has to
transmit a single bit to tell you what's wrong. Anything that *does* transmit — actively probing a
device to confirm it's alive, for instance — is a deliberate, clearly separate action, not
something that happens by accident while I'm just trying to look at my own network.

## Where this starts

That's the shape of it: a cheap radio doing the listening, a small host doing the thinking, and a
dashboard built around answering "what's actually wrong" instead of just "what happened." The idea
I want on record before any of the harder engineering starts: **diagnosis, not just detection.**
Time to go build it.
