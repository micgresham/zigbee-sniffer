# The Satellite That Wouldn't Update

I did not expect firmware updates to be the hard part. The satellites are wired up, the SPI relay
works, captures flow both directions — updating a satellite over that same link should be the easy
victory lap. It has instead been the hardest bug of the whole project, and it's worth writing down
while it's still fresh, because the shape of it is genuinely strange: **the fix for corrupted OTA
transfers was itself a firmware update, and installing it needed an uncorrupted transfer to
arrive.** A chicken-and-egg problem, for real, not just a figure of speech.

## Every transfer, eventually, corrupts

The symptom: push a firmware image to a satellite over SPI, and somewhere in the transfer, the
image comes out wrong. Not every time in the same place, not always fatally, but often enough that
"eventually" is really "almost always" once an image is more than a couple hundred kilobytes.

SPI over a real physical link drops things sometimes — a reply gets lost, the host doesn't hear a
chunk's acknowledgment in time, and it does the only reasonable thing: resends that chunk. That's
supposed to be harmless. It is not harmless. The satellite's OTA write path had no way to tell a
**resent** chunk from **new** data — it just appends whatever it receives, in order, wherever its
internal write pointer currently is. Resend a chunk, and it gets written a second time, shifting
every byte after it over by one chunk's length. The image downstream of that point is now garbage,
and nothing detects it until the very end, when the device tries to boot the result and can't.

Somewhere in the middle of chasing that, a second, unrelated bug showed up: erasing the whole flash
partition in one call before writing anything, which seemed like the obviously-correct way to do
it, is a single long blocking operation — long enough, on this single-core chip, to starve the idle
task that the hardware watchdog is monitoring. The device would just reset, silently, mid-transfer,
with no crash log because nothing actually crashed — the watchdog did exactly what it's supposed to
do.

## The fix, and the trap

Both got fixed properly. The satellite now writes straight to the raw flash partition instead of
going through the higher-level OTA API's own internal, stateful write-pointer tracking — every
chunk carries its own byte offset, and the satellite keeps track of how much it's actually applied,
so a resent chunk gets recognized and *skipped*, never double-written. Flash erasing happens
incrementally, one sector at a time as data actually arrives, instead of one long upfront blocking
call that trips the watchdog.

Except: getting that fix from source code onto an actual running satellite means an OTA
transfer — the exact thing that's broken. Every attempt to update a satellite to the fixed firmware
was itself vulnerable to the bug the fix was supposed to solve, because the satellite currently
running had no idea how to survive a resent chunk. Fixing OTA over OTA doesn't work when the thing
being fixed is the very thing doing the delivering.

The way out was the boring, obvious one: a manual USB reflash, once, by hand. Even that had its own
detour — the standard "hold BOOT, tap RESET" trick to force a chip into its bootloader dropped it
into its *native* USB download mode, which turned out to be a completely different electrical path
than the external UART adapter I'd been using to watch its console logs the whole time. Wrong port,
wrong assumptions, a bit of head-scratching before realizing the fix was simply plugging the
satellite's own USB port directly into the computer instead.

## Proof

With the real fix actually running on the hardware, the thing I actually set out to test — updating
a satellite over the SPI carrier link, the normal way, no cables, no manual reflash — got to run for
real. Twice. A push from one version to the next, and then another push right after that, both
needing constant chunk retries the entire way through — this link really is lossy, that part was
never in question — and neither run produced a single corrupted byte.

That's the part that matters: not that the link is clean, but that it doesn't have to be, anymore.
