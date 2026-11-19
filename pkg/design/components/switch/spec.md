# Switch
On and off for a setting that applies at once.

Matched macOS 26 NSSwitch, measured at 2x on a Mac: track 2.2:1 with a capsule knob 1.6:1 (not a circle), inset 2 pt; sizes 36x16 (System Settings rows), 44x20 (default), 64x28 (macOS large = iOS 63x28). Off track 10% ink, no border. Dark knob is light gray, not white. No hover change. Pressed knob swells from its outer edge to 1.28 x 1.5, past the track. Disabled fades the track to half; the knob stays solid. Focus ring follows the capsule. See items/switch/NOTES.md.

## CSS notes
Switch: <input type="checkbox" class="switch" role="switch">. Native input, so Space, forms and labels work.
macOS 26 / iOS 26 geometry: track 2.2:1, capsule knob 1.6:1, inset 2 (1.5 at sm). Sizes: .sm 36x16 (System Settings rows),
default 44x20 (macOS small), .lg 64x28 (macOS large = iOS). No hover change (Apple has none). Pressed: the knob swells
past the track from its outer edge (solid, not the glass lens). Disabled fades the track only; the knob stays solid.

## Choices
- Chosen: Default 44x20, the Settings 36x16 as .sm for dense rows, 64x28 as .lg for touch. Other option: Default 36x16 like System Settings. (Our body text is 14 px (Settings is 13 pt) and the web needs a larger hit area; all three are real Apple sizes.)
- Chosen: Pressed knob stays solid white and swells. Other option: Apple's clear lens knob that shows the track through it. (No glass on the web. Geometry is Apple's.)

## Measured
## Measured (macOS 26.6 NSSwitch on a Mac at 2x, pt)
| size | track | knob | inset |
|---|---|---|---|
| large | 64x28 | 38x24 | 2 |
| regular | 54x24 | 32x20 | 2 |
| small | 44x20 | 25x16 | 2 |
| mini / SwiftUI in a grouped Form (System Settings) | 36x16 | 20-21x13 | 1.5 |
| iOS 26/27 (shot) | 63x28 | ~39x24 | 2 |
- Track 2.2:1 at every size; knob is a capsule 1.6:1.
- Off track: black ~10% (light, #E4 on white), white ~9% (dark). No border.
- Knob: white (light). Dark: #E2E3E3 off, #EFF1F9 on (slightly brighter when on). Soft shadow: dark rim at the knob edge, fades ~4 pt below.
- Hover: no change.
- Pressed: knob swells from its outer edge (right edge fixed when on, left when off) to 1.28x width, 1.5x height, past the track top and bottom. It turns into a clear lens (glass) even with Reduce Transparency.
- Focus: 3 pt ring, accent ~50%, offset 0, capsule shape.
- Disabled: track at ~50% (on: accent half; off: fill half); knob unchanged; label dims.
- Slack c-switch (second ref, not taken): 54x30 / 44x24 / 36x20, round knob, check glyph when on, 1 px outline when off, gray knob when off, hover darkens outline, focus 1 px + 5 px 30% halo, 80 ms ease-in.

## Before -> now
- Knob circle 18x18 -> capsule (25x16 at default). The main miss.
- Track 38x22 -> 44x20 (Apple small). Added .sm 36x16 (Settings) and .lg 64x28 (macOS large = iOS); old .sm 30x18 was not an Apple size.
- Off track 15% ink -> 10% ink.
- Hover darkened the off track -> no hover change (Apple).
- Pressed widened the knob to 24 px inside the track -> swells 1.28 x 1.5 past the track from the outer edge, stays a capsule (width/height animate, not scale).
- Disabled opacity .4 on the whole control -> track at half, knob solid.
- Dark knob white -> light gray via color-mix with the surface (white in light).
- Increase contrast outline only on the off track.
- Demo: sizes rows and a System Settings style group with real labels, a note and a disabled row.

## Open
- Focus ring: tokens give 3 px with 1 px offset; Apple has offset 0. Waiting for the ds focus decision (HOLD).
- Pressed lens: we keep a solid knob (no glass). If the reviewer wants the lens feel, a 15% transparent knob would show the track through it.
