# Search field
Find in a list or the whole app.

Matched macOS 26 search fields (System Settings 28, Messages 36): fill capsule, no hover change, clear button only with text, a pale wide focus ring with no inner line. Every Mac search field shows that ring; only Slack (a popover) and iOS show none.

## CSS notes
Search field: macOS 26 capsule. Magnifier left, clear button only with text, soft focus ring, no hover change.
<label class="search"><mp-icon name="search"></mp-icon><input type="search" placeholder="Search"><button type="button" class="clear" aria-label="Clear" tabindex="-1"></button></label>
Sidebar 28 (default, System Settings), .lg 36 (Messages). Width comes from the layout (flex: 1 in a sidebar top bar). search-field.js: the clear button and Esc empty the field.

## Measured
## Measured (macOS 26, 2x px / pt)
- Sizes: System Settings sidebar 28 pt, Messages sidebar 36 pt, Finder toolbar 38 pt. Capsule.
- Fill: Settings #e8e8e8 on #fafafa (black ~7%); dark #303136 on #1c1f27 (white ~9%). Messages lighter (~3%). Finder: glass, turns white on focus.
- Hover: none (Settings fill identical with pointer over it). Slack: none.
- Focus: ring on every Mac search field (Finder, Settings, Messages, Safari). ~3.5 pt wide, straddles the edge (about 2 pt out, 1.5 pt in), pale accent (#c0c9e3 on white with a Mac's accent), no inner accent line. Animates in from a larger ring, ~0.2 s. Slack and iOS: no ring.
- Clear: filled gray circle with a cut-out x, ~14 pt, only with text (Messages shows it as soon as focused).
- Magnifier: glyph ~10.5 pt, ~9.5 pt from the left edge; text starts ~29.5 pt in (28 pt field).
- Text: 13 pt in Apple; ours stays 14 px body to match text-field.

## Ours vs ref: all fixed
- Ring was 3 px at 55% accent outside the edge (read as an outline): now 2 px out + 1.5 px in at 34% accent (light) / 62% (dark, via light-dark()), settles in from a 7 px transparent ring.
- Hover darkened the fill: removed.
- No clear button although "matched" claimed one: added (mask circle, text-3, hover text-2, press text), search-field.js wires click and Esc (Esc on empty blurs).
- Icon 16 -> 14 (md), 16 (lg); padding 8 / 12, gap 5 / 7.
- Disabled row added (opacity .45, no clear). Fixed widths 220 / 260 in the demo only; in layouts width comes from flex.
- Dropped a duplicate @keyframes mp-spin (push-button defines it).

## Open
- One view is that Apple has no focus ring on fields. macOS 26 does (soft); we kept a soft ring. If he means iOS or Slack, drop the box-shadow on :focus-within.

## More CSS notes
- Focus: Apple's ring is wide and pale, half over the edge, with no inner line; it settles in from a larger ring.
