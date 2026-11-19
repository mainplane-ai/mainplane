# Segmented control
Switch between a few views or filters. In content and forms: the cornered regular control. Capsule only at large size, in a toolbar, and on touch.

Matched macOS 26 by size, as measured: regular 24 px with a continuous 6 px corner, small 20 px with 5 px, and the selected segment fills the full height with the same corner (System Settings, forms). Large 28 px and toolbar 36 px are capsules with an inset capsule thumb (Finder view switcher, Calendar). 13 px regular text, same weight when selected, 1 px dividers hidden next to the thumb, no hover, press darkens. Ours: ink thumb where Apple uses the accent (blue is for needs you); gray thumb in the toolbar, as Apple.

## CSS notes
Segmented control: <div class="segmented" role="radiogroup"><button role="radio" aria-checked="true">...</button>...</div>
Shapes as macOS 26 (measured 2026-11-18, NOTES.md):
default = regular, 24 tall, continuous corner 6, the selected segment fills the full height with the same corner (System Settings, forms, filters).
.sm = small, 20 tall, corner 5. .lg = large, 28 tall capsule, thumb inset 2 (also the touch size). .in-toolbar = toolbar group, 36 tall capsule, thumb capsule inset 4 (Finder view switcher, Calendar).
Selected: ink thumb with paper text (Apple puts the accent here; blue is reserved for "needs you"). Toolbar: gray thumb, as Apple.
.fill stretches segments to the width. Icon segments: an <mp-icon> with aria-label.

## Measured
## Apple shape by place (measured at 2x, macOS 26.5.1)
| place | height | track | thumb |
|---|---|---|---|
| mini (NSSegmentedControl / Picker) | 16 | rounded rect, r ~4 continuous | full height, same corner |
| small | 20 | r ~5 | full height |
| regular (System Settings Output/Input, forms, the default) | 24 | r ~6 continuous | full height, same corner on all four sides |
| large | 28 | capsule | capsule, inset ~1.5 |
| extraLarge | 36 | capsule | capsule, inset ~1.5 |
| toolbar (Finder view switcher, Calendar Day/Week/Month/Year) | 36 | glass capsule | capsule 28, inset 4, gray even in a key window |
| iOS 26 | ~32 | capsule, #EEEEEF on white | white capsule, inset 2, no visible shadow |
- So "pill vs cornered": the reviewer is right for the content control. The old item was a 28 px pill with an inset raised thumb, which is the iOS / large-size look.

## Detail list (Apple -> ours)
- Corner: regular continuous 6 -> --radius-6 (squircle k 1.78). Small 5 -> calc(5px * k). Large/toolbar capsule -> radius-full + corner-shape round.
- Thumb: full height, no inset, same corner (regular/small). Inset 2 at large, 4 in the toolbar.
- Track: ~7.5% black (#ECECEC on white, #E4E4E4 on #F7F7F7); dark #2F2F2F on #1E1E1E -> --color-fill.
- Selected fill: key window accent #0077F9 + white text; inactive gray (#CFCFCF light, #494949 dark). Ours: ink thumb + paper text (dark: paper thumb + ink text). Apple with Graphite accent also uses a solid neutral thumb with white text; blue is reserved for needs you.
- Toolbar thumb: gray (#DEDEDE on the glass) -> --color-fill-pressed on --color-floating with the floating edge and shadow-1 (as toolbar .tb-group).
- Text: 13 regular, label color, same weight selected and not (no width jump). Small 12 (Apple 11, too small in Red Hat Text).
- Padding: ~10 pt each side of a label (regular); icon segment 27-28 wide.
- Dividers: 1 pt, inset 5 pt top and bottom (13-14 of 24), #D5D5D5 / #CECECE light, #434343 dark -> --color-separator; hidden on both sides of the thumb.
- Hover: none in AppKit -> none. Press: unselected segment darkens -> --color-fill-pressed.
- Focus: accent ring on the focused segment, following its corner.
- Disabled: text tertiary, thumb a light gray fill.
- Motion: Apple slides the thumb between segments. Ours crossfades (150 ms); a sliding thumb needs JS in the real app.

## Open
- Touch/mobile: iOS uses a ~32 capsule with a white thumb; .lg (28 capsule) is the closest we have. Add a touch variant when the mobile app starts.
