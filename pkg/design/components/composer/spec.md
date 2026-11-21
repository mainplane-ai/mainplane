# Composer
Always there. + opens Camera, Photos, Files. Files sit in the pill above the text. Mic until there is text or a file, then send. Grows upward; + and send stay on the last line.

Matched Messages on macOS 26 (files inside the pill above the text, no focus ring) and Slack (one wrapping row of chips, square image thumbnails, remove circle on hover). Send is the one blue action.

## CSS notes
Composer (iMessage on macOS 26): + circle, a pill field that grows upward, the mic inside it until there is text or a file, then send.
Files sit inside the pill above the text (Messages); several go in one wrapping row (Slack); remove is a small ink circle on the chip's corner, shown on hover (Slack).
Pill: one radius, h/2 at one line (31), kept as it grows: a capsule at one line, a continuous rounded rect when taller (Apple fields). One line: round ends, a true capsule (a squircle clamped to h/2 looks boxy). Taller (.tall, set by composer.js): the same 15.5 corner as a continuous squircle where supported. Focus: no ring, the caret is the cue (Messages). One cue at rest: gray fill on the + circle and the pill, no hairline.

## Choices
- Chosen: Files above the text, inside the pill (Messages). Other option: Files below the text (Slack). (Reads in send order: the file, then what to do with it. The text stays next to send.)
- Chosen: Several files in one wrapping row; images as square thumbnails at chip height (Slack). Other option: Each file as a large card in a vertical stack (Messages). (Messages' stack pushes the text far down after two files. A row keeps the draft visible.)
- Chosen: Remove: a small ink circle with an x on the chip's top-right corner, shown on hover or focus, always on touch (Slack). Other option: No remove control: select the file and press Delete (Messages). (Delete on an inline file is not discoverable on the web.)
- Chosen: Chip one step darker than the pill (fill on fill), no border, icon on a paper tile. Other option: White card with a hairline (Slack). (Messages does the same (gray card on the pill). No new line color in the pill.)
- Chosen: Focus: no ring; the caret is the cue (Messages). Other option: Darker edge plus a faint shadow (Slack). (The composer is the only input on the screen and is focused almost always; a ring there is noise.)
- Chosen: A file with no text turns the mic into send. Other option: Keep the mic until there is text. (Sending a file alone is a real case. Slack enables send for it.)

## Measured
## Detail list (ref -> ours)
- Where files go: Messages inside the pill above the text; Slack inside the box below the text. Ours: above (choice).
- Several files: Messages stacks large cards vertically; Slack one row, images as squares. Ours: one wrapping row, image square at chip height (choice).
- Chip: Messages gray card on lighter pill, no border; Slack white + hairline. Ours: fill on fill, no border, 48 high, radius 12, inset 6 (concentric with the pill's 18), icon on a 32 px paper tile whose left edge lines up with the text (14 px).
- Name 13/500, meta 11 text-2 ("PDF · 1.2 MB").
- Remove: Slack 20 px dark circle on the top-right corner on hover. Ours: same, ink circle, on hover/focus, always on touch, fades 120 ms.
- Send: Slack enables send with only a file. Ours: mic becomes send when there is text or a file.
- + and send stay bottom aligned while it grows (both refs). Textarea scrolls past 140 px.
- Focus: Messages none (caret only); Slack darker edge. Ours: no ring, accent caret (choice; text-field agent's finding).
- Round + and send: forced round in composer.css (icon-button default is now a rounded rect).

## Open
- Messages dark with a file was not captured (lab.sh burst needs start on a Mac); older dark refs are on the page.
- helpers.mjs composer() (used by project-view) still has the old chip markup; its inline ghost x stays visible (no .x class). ds may switch it to the markup in demo.js (button class "btn icon x").

## Messages size (orchestrator 2026-11-18)
- From project-view NOTES: field 31 (radius 15.5), round + 30, gap 10, inner mic/send 25 at inset 3, text padding 6/5, left 12; chips radius 10, left padding 6 (icon tile lines up with the text). Dock 11 left/right/bottom is project-view's .dock, not in this item. Check: ok.

## More CSS notes
- Attachments: chips 48 high, inset 6 so their 10 radius is about concentric with the pill's 15.5 (31 tall, Messages).
