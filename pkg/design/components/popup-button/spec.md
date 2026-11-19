# Pop-up button
Pick one of many. Shows the current value. Native select: the system draws the open list (on a Mac the real menu over the button, a check on the current value).

Matched macOS 26 NSPopUpButton, measured at 2x: gray fill (the push button fill), no border, label in ink, up-down chevrons in the label color (8x13 at 28 px, 1.5 px stroke, 10 px from the edge), rounded rect at 24 and capsule from 28, regular-weight value with the chevrons in a 20 px circle in a Settings form, disabled fill at half and label at a third.

## CSS notes
Pop-up button: <select class="select">. Native, so the OS draws the list (on macOS the real NSMenu over the button, with a check on the
Sizes like .btn: .sm 24 (Apple regular: rounded rect r 5), (md) 28 and .lg 36 (Apple large and extra large: capsule).
The fill, label weight and press match .btn so the two sit together. Chevrons: Apple's up-down pair in the label color, about 8x13 at 28.
.plain: the System Settings form value: text, then the chevrons in a 20 px gray circle. For a list with icons use a .btn that opens a .menu.

## Choices
- Chosen: Apple look: gray fill, no border, up-down chevrons. Other option: Slack look: white field with a 1 px gray border, radius 8, one down chevron, list opening below. (Apple is the first reference, and the fill matches our push button, so a pop-up and buttons in one row read as one family. Slack's bordered field looks like a text input.)
- Chosen: Label weight 500, as the push button. Other option: Apple's regular (400) weight for the value. (Apple uses one weight for its buttons and pop-ups; our push button chose 500, so the pop-up follows it to stay the same family. The form style (.plain) uses 400 like System Settings.)
- Chosen: Native select (the OS draws the open list). Other option: A custom .btn that opens our .menu (same look on every OS, icons in rows). (On a Mac the native list is the real macOS menu over the button, on phones the system picker, with keyboard and screen readers for free. Use .btn + .menu only when rows need icons.)
- Chosen: Gap of about 14 px between the value and the chevrons. Other option: Apple's 18-23 px. (Apple's gap comes from sizing to the longest item plus the check column. A native select also sizes to its longest option, so the extra space appears anyway when values differ in length.)
- Chosen: On touch screens: 36 px tall, 16 px text. Other option: Keep 28 px and 13 px. (iOS zooms the page into a control with text under 16 px when it opens, and 28 px is under Apple's touch size.)

## Measured
## Measured (macOS 26 NSPopUpButton, 2x shots, points)
- Heights mini 16, small 20, regular 24, large 28, extraLarge 36. Text 13 pt regular at regular/large/xl, ink #242424 (dark #DFDFDF).
- Regular 24 is a rounded rect (about 5 pt continuous); large and up are capsules.
- Fill #ECECEC on white (black 7.5%); dark #2F2F2F on #1E1E1E. No border, no shadow.
- Chevrons: up-down pair in the label color, not gray. Regular 7 wide x 10 tall (each 4, gap 2), stroke about 1.2; large 8 x 13, stroke about 1.4. Right edge 9 pt (regular) / 11 pt (large) from the button edge. Centered vertically.
- Left padding 12 (regular), 14 (large). Text to chevrons 18-23 (sized to the longest item).
- Disabled: fill about half (#F5F5F5), label and chevrons about a third (#B6B6B6).
- Settings form (SwiftUI grouped Form): value in regular weight, right aligned; chevrons in a 20 pt circle, fill #E4E4E4 on the #F7F7F7 group (dark #353535 on #252525).
- Open: the menu is as wide as the button and puts the current item over the button with a check.
- Pull-down: one small down chevron (6 pt wide). Not built: a pull-down is a .btn + .menu.

## Detail list (was -> now)
- Chevrons 4.7 px wide, gray text-2 -> 8x12.5 at 28 (8.4/11.5 at 24/36 scaled), 1.5 stroke, label color; disabled at 30%.
- Chevron inset 8 -> 10 px (8 at 24, 13 at 36). Right padding 28 -> 34 (29 / 42), gap to the value about 14.
- Small 24 capsule -> rounded rect var(--radius-6) (Apple regular; orchestrator decision for 24 px buttons).
- Left padding 12 -> 14 at 28 (12 / 18). Large text 14 kept.
- Disabled: whole opacity .4 -> fill 50%, label 32%, chevrons 30% (Apple).
- Press: instant (0 s) then fades back, like .btn; :open keeps the pressed fill while the native list is open.
- .plain (form value): gray text-2 on transparent -> ink, 400 weight, 14 px, right aligned, chevrons in a 20 px fill circle that darkens on hover/press.
- Long values: ellipsis, max-width 100%.
- Removed `option { color: initial }` (made black options on dark lists in Chrome on Windows).
- Dark chevrons also under prefers-color-scheme dark without data-theme.
- Touch (pointer: coarse): 36 px, 16 px text (no iOS focus zoom).
- Demo: three sizes, hover, pressed, focused, disabled, long value, next to push buttons, in a .form-rows card.

## Open
- Focus ring is ds's token (on hold).
- Weight 500 follows push-button; Apple uses 400. If push-button moves to 400, change --fs weight here too.

## More CSS notes
- .plain: System Settings form value. Regular weight, right aligned, chevrons in a 20 px circle that darkens on hover and press.

## Why the chevron has its own dark rule
A native <select> has no pseudo-elements, so the chevron is an SVG background, and an SVG in a data URL cannot read CSS variables. Its strokes are the text token (light #172428, dark #F8F8F5): change them with --color-text.
