# Push button
Capsule. One filled primary per view; gray secondary; plain; red only to destroy.

Matched macOS 26: one 13 px label size and one weight for every style (prominent is not bolder), capsules like the 28 pt large and alert buttons, padding 12/14/18 at heights 24/28/36, gray fill 7% ink, press darkens at once with no scale, disabled fades fill and label separately, spoke spinner. Slack: minimum widths 56/64/80 so short labels (Yes, OK) do not look like chips.

## CSS notes
Button: capsule at every size (macOS 26 large/alert buttons, iOS 26). Styles: (default) gray fill, .filled accent, .tinted, .plain.
Sizes: .sm 24 (rounded rect), (md) 28, .lg 36 (capsules). Roles: .danger. States: :hover/.is-hover, :active/.is-pressed, :focus-visible/.is-focus, :disabled, .is-loading.
One weight (500) for every style, as Apple uses one weight for bordered and prominent. Press darkens at once and fades back; no scale.

## Choices
- Chosen: 24 pt buttons are rounded rectangles (6 pt continuous corner); capsules from 28 pt. Other option: Capsule at every size. (Apple macOS 26 small controls (review round 1).)

## Measured
## Detail list (ref -> was -> now)
- Weight: Apple uses one weight for bordered and prominent -> filled/tinted 600, others 500 -> 500 for all.
- Label size: macOS 13 pt at 24, 28 and 36 -> sm 12 -> sm 13 (md 13, lg 14 kept: web reading size at 36).
- Padding: macOS ~12/14/18 at 24/28/36 -> 10/12/16 -> 12/14/18.
- Min width: Slack 56/80/96; macOS short buttons are never chips -> none -> 56/64/80 (not on .btn.icon; alert sets min-width 0).
- Press: macOS darkens with no scale (iOS 26 glass grows, never shrinks) -> scale .96 with spring -> no transform; darkens at once (0 s), fades back 150 ms on release.
- Disabled: Apple fades fill and label separately (prominent fill 43%, label dimmer) -> whole element opacity .4 -> fill 50% (filled 42%), label 38% (filled 55%).
- Loading: was a C arc and a hidden "Saving" label (width changed) -> the Apple 8-spoke spinner (same as the spinner item) in the label color, over the real label so the width holds.
- Icons: none shown -> "With icon" row; icon gap 5, outer side 3 px tighter so the padding looks even.
- Large row wrapped Delete onto a second line -> 3 buttons, no wrap. Added "In an alert" row (equal-width, 8 gap like the macOS 26 alert).
- Text centering: measured cap-height center within 0.5 px of the button center. No change.
- Kept: capsule, gray fill 7% ink (macOS 7.5%), tinted red Delete (matches macOS 26 destructive alert), hover states (Apple has none; the web needs them), focus ring tokens (ds).

## Open
- Focus ring (3 px, 1 px gap, ds tokens) is heavier than Slack's 1 px + soft halo; ring token is ds's.

# Icon button
A button with only a symbol. Rounded rect, concentric with its toolbar; circle only for send and for close in sheets. Always has an aria-label and a tooltip. Ghost in toolbars, gray fill when it stands alone, blue only for send.

Matched Slack icon buttons (28 px, radius 8, fill on hover) for shape; Finder toolbar (macOS 26) for the glyph: about .44 of the button, 80% ink at rest, selected = 10% ink fill. Messages: circle send with a heavier glyph, round close. Sizes 24 / 28 / 36, radius 6 / 8 / 10.

## CSS notes
Icon button: a .btn with only an <mp-icon>. Needs push-button's .btn. Give it aria-label and data-tip.
Shape: rounded rect (Slack's 28 px / 8 px icon buttons, squircle where supported). Radius --ib-r: 6 / 8 / 10 at 24 / 28 / 36.
Inside a rounded container set --ib-r to the container radius minus its inset, so the fills are concentric.
Circle only where Apple uses one: .filled (send) and .round (close in a sheet or banner).
Glyph about .44 of the button (Finder search 16 in 36, Messages plus 13 in 30) and 1.1-1.3 px lines (SF Symbols Regular).
.ghost: no fill until hover (toolbars, message hover actions). Glyph at 80% ink like the Finder toolbar (#4C4C4C), full ink on hover and press.
Selected (aria-pressed="true" or .is-selected): the Finder view-switcher fill, 10% ink / 14% paper, full ink glyph, no hover change.

## Choices
- Chosen: Rounded rect (squircle) icon buttons, radius 6/8/10, --ib-r set to container radius minus inset so fills are concentric. Other option: Circles at every size (macOS 26 / iOS glass buttons).
- Chosen: Circles only for send (.filled) and close in sheets and banners (.round), as Messages and macOS sheets. Other option: Rounded rect for those too.
- Chosen: Hover fill on ghost buttons and the glyph darkens to full ink (Slack). Other option: No hover feedback (Finder toolbar).
- Chosen: Disabled send: gray circle with a faint glyph in both themes. Other option: Faded accent (Apple light #92C6FF); it turned a muddy teal on dark.

## Measured
## Detail list (ref -> ours before -> now)
- Ghost glyph color: Finder #4C4C4C (70% black), Slack #454447 -> ours text-2 #65757A (too light, teal) -> 80% ink. Hover and press: full ink (Slack darkens; Apple has no hover).
- Ghost pressed glyph went back to text-2 (lighter than hover) -> bug, fixed: pressed = full ink.
- Selected/on state (Finder view switcher): 10% ink fill, 14% paper in dark, full-ink glyph, no hover change -> missing -> added (aria-pressed="true" or .is-selected).
- Glyph to circle ratio: Apple about .44 (Finder search 16/36, Messages plus 13/30, zoo 10.5/24) -> ours varied: sm .67 box (too big), lg 20 px box on 36 (too small) -> icon box 15 / 17 / 22 for 24 / 28 / 36 (plus glyph .41-.42, search .45 at every size).
- Line weight: Apple Regular 1.1-1.3 px -> lg was 1.63 px -> lg stroke 1.25 (1.32 px). sm 1.1, md 1.26 px.
- Send (filled) glyph: Messages/iOS arrow is heavier than toolbar glyphs -> ours thin 1.25 px -> stroke 2 (1.6 px at 28).
- Spacing in a toolbar run: Finder 36 pt pitch, about 2 pt between hit areas -> .ib-run (gap 2px) shown in "In a toolbar" rows.
- Tooltips: rule says always -> demo buttons now carry data-tip (tooltip item's [data-tip] CSS shows it on real hover).
- Disabled ghost glyph: Finder 25% black -> 80% x 38% = 30% ink. OK.

## Left as is (not mine)
- Focus ring (3 px, 1 px offset, from push-button .btn). Apple's ring hugs the shape; the 1 px gap reads fine on circles.
- Disabled filled (send) in dark is a muddy teal (push-button's .btn.filled:disabled).
- Ghost in dark at 80% is a little dimmer than Finder's #E8E8E8 (91%); one mix for both themes keeps it simple.

## Round 2 (feedback/2026-11-18-round1.md)
- Shape: rounded rect (squircle), --ib-r 6/8/10 at 24/28/36 (Slack 8 at 28). Concentric: in a container set --ib-r: calc(container radius - inset) (.ib-group demo, radius-10 minus 3). Toolbar agent uses this.
- Circles kept only for .filled (send) and .round (close in sheets/banners).
- Disabled send: gray fill circle + 32% ink glyph in both themes (was a muddy faded teal on dark).
- item.json choices: [{chosen, other}] (build does not render them yet; ds owns build).

## More CSS notes
- disabled send: a gray circle with a faint glyph (as disabled send in chat apps), not a faded accent that turns muddy on dark
