# Slider
A value in a range. Steps get tick dots; ends get captions or small/large glyphs.

Matched macOS 26 NSSlider, as measured at 2x: 6 px capsule track, 20x16 capsule knob (small: 4 px track, 18x14 knob), empty track at 10% ink, fill that ends under the knob centre, white knob with a soft 0.5 px edge and low shadow, a light gray knob in dark (Apple's is #DFE0E1, not white), disabled keeps the knob and only grays the fill, no hover, tick dots 2 px and 3 px under the track at the knob-centre positions.

## CSS notes
Slider: <input type="range" class="slider">. Measured on macOS 26 NSSlider (items/slider/NOTES.md).
Regular: 6 px capsule track, 20x16 capsule knob. .sm: 4 px track, 18x14 knob. .ticks: 2 px dots 3 px under the track, one per step (set --steps).
Fill: graphite (ink at 56%, paper at 62% in dark), not the accent (blue is for "needs you"), and it runs to the knob centre. slider.js keeps --v (0..1) in sync.

## Choices
- Chosen: Graphite fill (ink at 56%, paper at 62% in dark). Other option: Apple's accent fill (our blue), or full ink like the segmented thumb. (Blue is for needs you. Full ink made a 6 px bar heavier than anything near it; graphite is what macOS draws with the Graphite accent.)
- Chosen: Pressed as measured: the knob grows 1.25x (20x16 to 25x20), drops its shadow and becomes a lens; the fill gets stronger. Our lens is solid: a faint surface tint with a hairline edge. Other option: Keep the white knob and only grow and lift it. (macOS 26 makes it a clear glass lens; no glass on the web, so the solid tint is the Reduce Transparency version of the same thing.)
- Chosen: Regular and small sizes only. Other option: Apple's mini (16x12) and large (24x20) too. (Forms and sheets need two sizes; the others can come with a real use.)

## Measured
## References
- Colors from the key-window RefZoo shots (2x).
- Slack: no slider anywhere in the desktop app we use (prefs use selects and checkboxes). Apple only.

## Measured (macOS 26, points)
| | Apple | Ours before | Ours now |
|---|---|---|---|
| Regular track | 6 capsule | 6 | 6 |
| Regular knob | 20x16 capsule | 22x16 | 20x16 |
| Small track / knob | 4 / 18x14 | 6 / 18x14 | 4 / 18x14 |
| Mini track / knob | 4 / 16x12 | none | none (YAGNI) |
| Large track / knob | 6 / 24x20 | none | none (YAGNI) |
| Empty track light | black 10% | ink 15% | ink 10% |
| Empty track dark | white 10% | paper 20% | paper 10% |
| Fill | accent, to the knob centre | accent, to val% of track (drifts off knob centre up to 10 px) | graphite (ink 56% / paper 62%), to the knob centre |
| Knob light | #FFF, 0.5 edge, shadow ~8% 1-4 px below | #FFF, shadow .25 tight (dark rim) | #FFF, softer shadow |
| Knob dark | #DFE0E1 (not white) | #FFF | #E4E7E6 |
| Disabled | fill turns gray (black 12%), knob unchanged, full opacity | whole control at 40% | fill ink 16%, knob unchanged |
| Hover | none | none | none |
| Ticks | 2x2 dots, 3 below the track, black ~25%, at knob-centre positions | none | .ticks, --steps |
| Pressed | knob grows 20x16 to 25x20 (1.25x) into a clear lens, no shadow; fill darkens (light) / brightens (dark) | scale 1.15, white | 1.25x, solid lens (text 6% on surface, hairline edge, no shadow), fill 68% / 78% |
| Focus | accent ring around the knob | 3 px ring on knob | same, uses the focus tokens (pending ds) |

## How the pressed state was captured
- ref-lab/sliderzoo/live.sh: takes the GUI lock, lab.sh start, runs SliderZoo on the virtual display and holds a mouse-down mid-drag with in-process NSEvents (no CGEvents, so it works while the session is locked), screencapture -l, then releases. Focus rings do not draw there (inactive window, keyboard navigation off).

## Open
- Focus ring follows the shared focus tokens (ds decision pending).

## More CSS notes
- Pressed (measured): the knob grows 1.25x and turns into a clear lens with no shadow; the fill gets stronger. Our lens is solid: a faint tint of the surface with a hairline edge.
- In a form (System Settings Dock size, Display text size): end captions under the track ends, or small/large glyphs at the sides.
