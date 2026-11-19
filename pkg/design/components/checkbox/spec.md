# Checkbox
Several independent choices. Use a switch for a setting that acts at once, a checkbox for choices in a list or form.

Matched macOS 26 NSButton checkbox, measured at 2x: 16 pt box (small 14), no border or shadow, flat 10 % fill when off, accent fill with a white check when on, 6 pt to the label, 3 pt focus ring on the box edge. The reference Mac's accent is set to lavender; ours is the brand blue.

## CSS notes
shared label row for switch, checkbox and radio. 6 px from box to label (macOS 26 NSButton: title at x 22 of a 16 pt box).
With a description (<span>Label<small>hint</small></span>) the box aligns to the first line and the hint sits under the label.

## Choices
- Chosen: A quiet hover step (fill 10 % to 14 %, accent to accent hover). Other option: No hover, as in macOS. (On the web the pointer needs a sign that the box and its label are one target. Slack also changes on hover.)
- Chosen: Check appears in 120 ms (fade and scale from 60 %). Other option: Instant, as in macOS; or the old bouncy spring. (Close to native, but it does not blink. The spring felt like a toy next to Apple's controls.)
- Chosen: Apple's flat filled box. Other option: Slack's bordered box (native Chromium, 13 px). (Slack's is the stock web checkbox: a gray border and a heavy blue. Apple's reads calmer in long forms and matches our other controls.)

## Measured
## Detail list (macOS 26 = reference; done = fixed in checkbox.css)
- Box size 16 pt (small 14). Ours was 16, the "matched" text wrongly said 14. Added .sm 14. done
- Off: flat 10 % ink fill, no border, no shadow. Ours had a 28 % inset ring and shadow-1 (a web-form look). done
- Corner: profile [6,4,3,2,1,1,0] px at 2x. Ours 4.5 px was [5,3,2,1,0]; now 5 px (squircle k) gives [6,3,2,1,1,0]. done
- Check glyph: bigger and lower than ours; centerline (3.9,8.6)(6.6,11.8)(11.9,4.6) in 16, stroke 1.9 (Chrome AA reads thinner than 1.8). done
- Mixed dash: 6x2 pt (ours was 7.8 wide). done
- Label gap 6 pt (ours 8). done (.check-row gap 6; radio and switch share it)
- Hint text: box aligns with the first line, hint under the label (Finder "Sync Desktop & Documents"). Ours centered the box on both lines. done (.check-row:has(small))
- Pressed: off fill to 19 %; on: accent-fill-pressed. Ours was brightness(.94), almost invisible. done. Note: macOS lightens "on" when pressed in dark; we use the token (darker), like push-button.
- Hover: none in macOS; we keep a quiet step (choice listed in item.json).
- Focus: 3 pt ring at about 50 % accent, offset 0, outer corner = box radius + 3. Ours had offset 1 px; now 0 for the checkbox. Width and color stay on the shared tokens (pending decision in STATUS.md).
- Disabled: same drawing at 45 % (off 5 %, on accent 45 % with white check); label tertiary. done
- Motion: macOS instant; ours was a 250 ms bouncy spring, now 120 ms ease-out fade+scale; none under reduced motion. done
- Increased contrast: 1 px 55 % ink edge on the off box (media and data-contrast). done
- Inactive window gray accent: not applicable to the web.
- Indeterminate in static HTML: checkbox.js sets input.indeterminate for [data-indeterminate].
- Demo: states with on/off/mixed, small, with a hint, in a form.

## Open
- Label size: macOS uses 13 pt for a regular checkbox; ours uses --text-body 14 px (the page scale). Kept.

# Radio button
One choice of a few, all visible. Options in a row 18 px apart, or a column 6 px apart with an optional line of explanation.

Matched macOS 26 (SwiftUI and AppKit radios, measured at 2x): flat 16 px circle with no border or shadow, 10% ink when off (light and dark), accent fill with a 5 px white dot when on, 6 px to the label, 18 px between options in a row. Press deepens the fill at once (lighter in dark, as on the Mac). Disabled fades the control to 45% and the label to tertiary.

## CSS notes
Radio: <input type="radio" class="radio"> in a .check-row label. macOS 26: flat 16 px circle, 10% ink when off,
accent with a 5 px white dot when on, no border or shadow. .radio.sm is 14 px. Options sit in a .radio-group
(row, 18 px apart) or .radio-group.v (column, 6 px apart; a <small> under a label explains the option).

## Choices
- Chosen: Flat 10% ink circle for an unselected radio, as in macOS 26. Other option: A 1 px ring on white (Slack, Chrome, older macOS). (Apple dropped the ring in macOS 26 and it reads calmer next to switches and checkboxes. The ring has more contrast; we can add it under Increase Contrast.)
- Chosen: Hover lifts the fill (10% to 14%, accent to accent hover). Other option: No hover, as on the Mac. (On the web a pointer needs a sign that the label row is clickable.)
- Chosen: Label row gap 6 px (Apple 6 pt). Other option: 8 px, the shared check-row gap the checkbox uses. (Measured on macOS 26; the checkbox could follow (Apple 5 pt).)
- Chosen: Focus uses the shared ring tokens (3 px, 1 px gap). Other option: Apple's ring: 3 pt, flush on the circle, accent at about 50%. (The ring tokens are decided once for all controls (ds).)

## Measured
## Detail list (ref -> was -> now)
- Off fill: macOS 26 flat 10% ink, no border or shadow -> white + 28% inset ring + shadow-1 -> flat color-mix(text 10%) in both themes.
- On: flat accent, 5 pt white dot (16 pt circle) -> 6 px dot, bouncy spring -> 5 px dot (text-on-accent), plain spring (no overshoot on a 5 px dot).
- Size: 16 pt regular, 14 small -> 16 only -> 16 and .radio.sm 14.
- Press: Apple deepens the fill at once, lighter in dark -> filter brightness .94 (darkened dark mode too) -> off 20% ink (0 s), on accent-fill-pressed.
- Hover (web only): off 14% ink, on accent-fill-hover (was off only).
- Disabled: Apple fades the control about 50% and the label -> opacity .4 -> .45, hover cannot change it; label tertiary (check-row).
- Label gap: Apple 6 pt -> 8 (shared check-row) -> 6 on radio rows only.
- Option spacing: Apple 18 pt in a row, 6 pt stacked -> 10 (page row gap) -> .radio-group (18 / 6) and .radio-group.v with an optional <small> line; the radio aligns to the first line.
- Demo: added Small 14, Row of 3, and a stacked Group with explanations (a Notify me about setting).
- Kept: focus ring tokens (ds). Apple's ring is 3 pt flush (offset 0), accent about 50%; ours has a 1 px gap.

## Open
- Unselected 10% fill has low contrast (about 1.2:1). Apple accepts it. Increase Contrast could add a ring later.
- check-row gap: checkbox (owner checkbox agent) still 8 px; Apple 5 pt for checkboxes.

## More CSS notes
- Checkbox: <input type="checkbox" class="checkbox">, .sm for dense lists. Matches macOS 26 (measured on NSButton, 2x): 16 pt box (sm 14), no border, no shadow: a flat 10 % ink fill when off; accent fill and a white SF-style check when on. Pressed deepens the fill. Disabled is the same drawing at 45 %. Focus is a 3 pt ring hugging the box (offset 0). Indeterminate: input.indeterminate = true (or data-indeterminate, set by checkbox.js).
