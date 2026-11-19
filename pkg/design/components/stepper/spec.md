# Stepper
Small whole numbers in a known range (workers, retries). The value is text next to the stepper. For free numbers use a text field.

Matched the iOS 26/27 stepper, measured at 3x: a gray capsule (6.5 % fill) cut in two by a 1 px hairline that is 75 % of the height, a minus and a plus glyph of 0.42 x the height with a 1/16 stroke, the value outside it in the label's font with tabular figures. iOS size 32 x 93 is .lg; the default is 28 x 80 for the desktop. Press darkens the half you press (its outer end keeps the capsule round); the end button dims at min and max; press and hold repeats, as in NSStepper and UIStepper. One focus stop (spinbutton): arrow keys step, Home and End jump, one ring around the whole capsule, as macOS rings the whole NSStepper.

## CSS notes
Stepper (iOS 26/27 shape, measured): a gray capsule split into a minus half and a plus half by a short hairline.
The value is text next to it, not inside it (Apple puts it in the label).
<span class="stepper-field"><output>3</output><span class="stepper" role="spinbutton" tabindex="0" aria-label="Workers" aria-valuenow="3" aria-valuemin="0" aria-valuemax="10">
<button tabindex="-1" data-step="-1" aria-hidden="true"><mp-icon name="minus"></mp-icon></button><button tabindex="-1" data-step="1" aria-hidden="true"><mp-icon name="plus"></mp-icon></button></span></span>
stepper.js: click, press and hold (repeats), arrow keys, Home/End; dims the end button at min/max; aria-disabled="true" disables all.

## Choices
- Chosen: iOS shape (minus | plus capsule) on every platform. Other option: The macOS NSStepper: a 20 x 25 pt up/down pair next to a number field. (The macOS control has 12 pt high targets and needs an editable field; it fits dense Mac forms, not a web app that also runs on phones. Apple's own SwiftUI forms on iPad and visionOS use the iOS shape.)
- Chosen: Value next to the stepper (in the row, before the capsule). Other option: Value inside, between minus and plus (the old item, common on shop sites). (Apple never puts the value inside. Inside, the value and the buttons blur into one gray block and the width changes with the digits.)
- Chosen: A quiet hover on each half (fill 7 % more). Other option: No hover, as on iOS and macOS. (Same rule as the buttons: the pointer needs a sign which half it is on.)
- Chosen: Press darkens at once, fades back in 150 ms; no scale. Other option: iOS 26 glass grow on press. (No glass on the web. Matches the push button.)

## Measured
## Apple, measured
| | iOS 27 (3x) | macOS 26 NSStepper regular (2x) |
|---|---|---|
| shape | capsule 93 x 32 pt, two halves | rounded rect 20 x 25 pt, up/down halves |
| fill | #EEEEEF light, #2C2C2F dark | #ECECEC light, #2F2F2F dark |
| divider | 1 pt, 24 of 32 tall, #B9B9BC / #65656A | 1 pt horizontal, inset 2 pt each side, #D5D5D5 / #434343 |
| glyph | minus/plus 13.3 pt, 2 pt stroke, label color | chevrons 8 x 4 pt, #767676 / #A1A1A1 |
| value | in the label ("Workers: 3") | in a separate number field left of it (4 pt gap) |
| at min/max | end glyph dims to tertiary | no change |
| disabled | both glyphs tertiary, fill stays | chevrons fade, fill stays |
| press | pressed half darkens | pressed half darkens |
| hold | autorepeats | autorepeats |
| focus | none (touch) | ring around the whole control, arrow keys |

## Old item -> fixed
- Value was inside the capsule, 600 weight, between full-height separators -> value outside, regular body font, tabular, min 2ch (no width jump 9 -> 10).
- Separators ran full height -> one 1 px hairline, 75 % of the height, text-3 at 60 % (iOS ~27 % black).
- Hover/press drew a capsule inside the capsule (gap at the divider) -> each half fills flat to the divider, round only at its outer end.
- Glyphs 16 px with the 1.2 px icon stroke (thin, too wide) -> 0.43 x height (12 px at 28) with a ~1.75 px stroke.
- Disabled row was not disabled: stepper.js re-enabled the buttons on load -> aria-disabled="true" on the stepper; sync keeps both off; value text dims too.
- At max had no demo -> "At min, at max" row.
- Every button was a tab stop with a ring per half -> one spinbutton focus stop (aria-valuenow/min/max), arrows and Home/End, one ring around the capsule.
- No press and hold -> repeats after 400 ms every 80 ms; stops on release, leave, blur.
- Mouse press no longer focuses or selects text.
- Sizes: default 28 x 80 (desktop), .lg 32 x 94 (iOS exact, touch).

## Open
- Focus ring weight is the ds token (pending decision).
