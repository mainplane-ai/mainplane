# Tooltip
Names a control, mostly icon-only buttons, with its shortcut. Shows after 0.5 s of hover, at once on keyboard focus and when moving along a toolbar. Never holds an action or a long sentence.

Matched Slack for behavior and shape: tag centered under the control with a small gap, 80 ms fade, short delay, warm handoff between neighbours, hidden on press, shortcut inside the tag, dark tag with a hairline edge in dark mode. Apple where Slack is heavier: no arrow, regular-weight label (12 px medium, not 13 bold), compact ⌘ glyphs like the menus instead of key caps, 24 px tall instead of 32, left-aligned wrap at 240 px like the macOS help tag.

## CSS notes
Tooltip: names a control (mostly icon-only buttons) after a short hover. Solid, no glass, no arrow.
Light: ink tag with paper text. Dark: the solid floating fill with its edge (an ink tag would vanish on dark surfaces).
Centered under the control, 6 px gap, above it when there is no room. Live behavior: tooltip.js ([data-tip], optional [data-tip-key]).

## Choices
- Chosen: Ink tag with paper text in light mode; the solid floating fill with its edge in dark mode. Other option: macOS help tag: light gray box in both modes (what the old version copied). (A light tag on a light page reads like a stray label; the ink tag is clearly a transient overlay and matches Slack, which web users know. In dark mode an ink tag would vanish, so it takes the floating fill and edge, as Slack does.)
- Chosen: Anchored to the control: centered under it, 6 px gap, flips above at the window bottom, kept 8 px from the edges. No arrow. Other option: macOS: at the pointer, below and right of it. Slack: centered with an arrow. (The control, not the pointer, is what the tag names, and keyboard focus has no pointer. The arrow adds a shape for no information when the tag is centered on a small button.)
- Chosen: 0.5 s first delay; then 0 s for 0.6 s after a tag hides; at once on keyboard focus. Other option: macOS: about 1.6 s (measured in Finder). Slack: about 0.15 to 0.35 s. (Our tooltips mostly name icon-only buttons, so a 1.6 s wait means guessing. Under 0.3 s they flash while the pointer crosses the toolbar.)
- Chosen: Shortcut on the same line in 60% ink, menu glyphs (⇧⌘L). Other option: Slack: key caps on a second line, or (⌘F) in parentheses. macOS help tags show no shortcut. (One line keeps the tag 24 px tall, and the glyphs match the context menu.)
- Chosen: 12 px medium, padding 4/8, 6 px corner (squircle where supported), wrap at 240 px left-aligned. Other option: Slack 13 px bold, 8/12 padding, 8 px corner (32 px tall). macOS 11 pt regular, square corners, 18 pt tall. (Between the two: readable white-on-ink text without Slack's bulk; corners concentric with our 24 px small buttons.)

## Measured
## Feedback round 1
- "Not sure about this... how does apple / slack do tooltips." Studied both (Finder help tags on macOS 26.6, Slack 4.53 light and dark, measured), rebuilt, choices in item.json.

## Detail list (ref -> ours)
- Fill: macOS translucent light gray, square; Slack #1D1C1D light, #212428 + edge dark -> ink (--color-text) with paper text in light; --color-floating + --color-floating-edge in dark. Shadow --shadow-2 (Slack none, macOS small).
- Shape: macOS square, Slack radius 8 at 32 tall -> --radius-6 at 24 tall (squircle k).
- Text: macOS 11 pt regular #434547; Slack 13/16 bold -> 12/16 weight 500, letter-spacing .01em.
- Padding: macOS 2/7, Slack 8/12 -> 4/8. Max width macOS ~260 left, Slack 200 centered -> 240 left, text-wrap pretty.
- Shortcut: Slack key caps (black, r4, 20 tall) or "(⌘F)"; macOS none -> inline kbd, 60% currentColor, 6 px gap, glyphs like the context menu.
- Place: macOS at pointer (+19 pt down); Slack centered under control, arrow, 8 px -> centered, 6 px, no arrow, flip above, clamp 8 px from viewport.
- Timing: macOS ~1.6 s; Slack 0.15-0.35 s, fade 80 ms -> 500 ms, warm 600 ms (instant, no fade), fade in 80 ms / out 120 ms, none with reduced motion (--motion).
- Dismiss: leave, press (stays hidden until leave), Esc, scroll. Keyboard focus-visible shows at once. No touch.
- a11y: one role=tooltip element (#mp-tooltip); aria-describedby only when data-tip differs from aria-label.

## API
- Any control: data-tip="Label" [data-tip-key="⌘F"]. tooltip.js handles it (icon-button demos already carry data-tip).
- Static drawing in fixtures: <span class="tip-at [above|end]">control<span class="tooltip">Label<kbd>⌘F</kbd></span></span>.

## Open
- Toolbar .tb-group buttons have no .is-hover style (toolbar item css), so the drawn hover in the toolbar fixture shows no fill.
- Dark/theme override duplicates the token theme selectors in tooltip.css; a --color-tooltip / --color-tooltip-ink token pair from ds would remove that.

## More CSS notes
- the live tag (tooltip.js): fixed, never takes the pointer, 80 ms fade in, 120 ms out, none when warm or with reduced motion
