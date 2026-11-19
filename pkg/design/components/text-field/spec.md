# Text field
One line of text. Boxed in forms; plain in grouped rows.

Matched Apple field shape (Safari address, Messages composer, search fields): one variable --field-r = one-line height / 2 (14 px at md), so a one-line field is a capsule, and a taller field (text area, composer) keeps the same corner instead of growing rounder. macOS 26 focus: a soft accent ring about 3.5 pt that replaces the edge (Safari address, Notes, Messages and Settings search), no hard accent border, no hover change. Grouped rows (Settings > About > Name) and message composers (Messages, Notes body) get no ring at all: caret and selection only. Accent caret and pale accent selection.

## CSS notes
Text field: <label class="field"><span class="field-l">Name</span><input class="input"><small class="field-help">..</small></label>
Box: surface fill, 1 px line, md 28 / .sm 24 / .lg 36.
Corner (Apple fields: Safari address, Messages composer, search): --field-r = one-line height / 2, so one line is a capsule.
A taller field (text-area, composer) keeps the same --field-r, so it reads as a continuous rounded rect, never rounder.
One line: corner-shape round (Apple's continuous corner at r = h/2 is a true capsule; a squircle clamped to h/2 has squarish ends).
Taller: border-radius: calc(var(--field-r) * var(--corner-k)); corner-shape: squircle. Same visible corner, continuous curve.
A squircle clamps (squarish ends) while the box is shorter than 2 * r * k (50 px at md): keep round until then.
No hover change (Apple and Slack fields have none).
Focus (macOS 26 Safari, Notes, Messages and Settings search): a soft accent ring about 3.5 pt that replaces the edge.
No hard accent border. Caret and selection use the accent.
.plain: grouped form rows (Settings > About > Name): no box, right aligned, no ring on focus, only caret and selection.
Error: aria-invalid="true" (danger edge, soft danger ring on focus).

## Choices
- Chosen: One-line field: capsule with corner-shape round. Taller fields: same --field-r, scaled by --corner-k with squircle once the box is taller than 2 r k. Other option: Squircle at one line too (radius clamped to h/2) (A squircle clamped to h/2 has squarish ends and reads as a rounded rect, not a capsule. Apple's continuous corner at r = h/2 draws a true capsule (Safari address field).)

## Measured
## Focus treatment found in real apps (the question from the reviewer)
| App, field | Focus |
|---|---|
| Messages composer | none: caret only |
| Notes note body | none: caret only |
| Settings > About > Name (grouped row) | none: selection only, stays right aligned |
| Settings sidebar search | soft accent ring |
| Messages sidebar search | soft accent ring (+ clear button) |
| Notes toolbar search | soft accent ring |
| Safari address field | soft accent ring, fill gray -> white |
| Slack composer | edge darkens 13% -> 45% + faint shadow, no ring |
| Slack modal field, sidebar filter | 1 px accent edge + 4 px 30% halo |
So Apple rings every boxed field and search field, but never a composer or a grouped-row field. The ring is soft (about 30% accent), 3.5 pt, and replaces the edge (no hard accent border).

## Differences (ours before -> now)
- Focus: hard 1 px accent border + 55% ring -> soft ring only (focus token mixed to ~30% light / ~36% dark), 1 px inner + 3 px outer, no accent edge. Done.
- Hover: edge darkened -> no change (Apple and Slack fields have none). Done.
- Caret: default ink -> accent. Selection: default -> pale accent 22%. Done.
- .plain (grouped rows): got a box, ring and flipped to left align on focus -> no box, no ring, stays right aligned. Read-only plain value is secondary ink, editable is primary (Settings). Done.
- Added .form-rows (grouped Settings-style rows), .field-l label and .field-help (danger when invalid). Done.
- .lg uses body-large 15 px. Done.
- Increased contrast: the ring goes back to full --color-focus (soft ring is below 3:1, same as Apple).
- 2026-11-19: shape = Apple field. :root --field-r = control-md / 2 (14); .sm/.lg set their own h/2. One line: corner-shape round (capsule). text-area and composer reuse --field-r: taller = calc(r * k) squircle once taller than 2 r k, round below. Padding 12 (sm 10, lg 16) for the capsule ends.
- Not changed: heights 24/28/36, 1 px line, disabled fill.

## Open
- Composer focus (Slack darkens the edge, Apple does nothing) belongs to the composer item.
- Soft ring is under WCAG 3:1 non-text contrast; caret + ring together are the indicator. Full ring under increased contrast.

# Text area
Several lines of text. Grows with its content, then scrolls.

Matched macOS 26 TextField(axis: .vertical): the text field box, edge, ring and padding, so one line is the field height and each line adds one line height. Starts at 3 lines, grows to 10, then scrolls with a thin scroller. No resize grip. Labels sit on the first line, as in Settings forms.

## CSS notes
Text area: <textarea class="input textarea">. The text field box (needs text-field.css) at several lines, like macOS 26
TextField(axis: .vertical) with .roundedBorder: same edge, fill, radius and focus ring as the one-line field, the same
vertical padding, so one line is exactly the field height and every extra line adds one line height.
Grows with its content (field-sizing: content) from --min-lines (3) to --max-lines (10), then scrolls with a thin overlay-like
scroller. No resize grip (Apple has none; it grows by itself). Sizes .sm and .lg follow the field sizes.
Corner: --field-r from text-field at every size. Default (min 3 lines, always taller than 2 r k): calc(--field-r * --corner-k) with
corner-shape squircle, Apple's continuous corner. .short (starts at 1-2 lines): stays round like the one-line field (capsule).
Disabled, read-only, error, focus, corner and inline padding come from .input.

## Choices
- Chosen: No resize grip; the box grows with the text up to 10 lines, then scrolls. Other option: Slack's vertical resize grip (c-input_textarea resize: vertical) in the bottom right corner. (Apple's multi-line fields have no grip, and a box that grows by itself never needs one. The grip was the most visible stray detail.)
- Chosen: Padding 4 px top and bottom, 10 px sides: one line is exactly the 28 px field; minimum 3 lines (68 px). Other option: Slack: 8/12/4 padding, 15/22 text, fixed 104 px start height. (Matches macOS vertical TextField and lines up with a text field next to it. The old 70 px min (2.5 x 28) cut the box at 2.8 lines.)
- Chosen: Disabled and read only use the text field's looks (fill, no edge; quieter surface, no edge). Other option: Apple: disabled keeps the white box and edge and only grays the text. (One rule for every input. The text field agent owns those looks; if they change, the text area follows.)
- Chosen: No character counter. Other option: Slack shows characters remaining under the box. (No Mainplane text area has a limit yet (instructions, project description, replies).)
- Chosen: Corner: --field-r from the text field. A text area (3+ lines) uses calc(--field-r * --corner-k) with corner-shape squircle; .short (starts at 1-2 lines) stays corner-shape round, a capsule at one line. Other option: Switch round/squircle by live height with JS as the box grows, or a radius that grows with height. the squircle radius would be clamped and look squarish, so short boxes stay round. Same visible corner either way.)

## Measured
## Detail list (ref vs ours before -> now)
- Resize grip: Apple none, Slack has one. Ours showed Chrome's grip in every state, also disabled -> removed (resize: none).
- Growth: Apple vertical TextField grows by line to its limit, then scrolls. Ours grew to 240 px -> grows from --min-lines 3 to --max-lines 10 (in lh), then scrolls. Both are custom properties per use.
- Line grid: Apple 5 pt pad, 16 pt lines, one line = field height. Ours min 70 px = 2.8 lines, padding 7 px (first line 3 px lower than in the text field) -> padding 4 px (sm 3, lg 7), so one line = 28/24/36 field height and min = 3 lines (68 px).
- Line height: ours hard-coded 20 px for every size -> taken from the size's text token (sm 13/18, lg 15/22).
- Scrolling: no hint at the limit -> fixture shows it scrolled to the end (text-area.js), the previous line cut at the top edge; thin scroller, text-3 thumb, transparent track (overlay on macOS).
- Disabled: demo said "Read only" inside a disabled box -> separate Disabled and Read only rows, both from .input (text-field owns the looks).
- States added: Error (aria-invalid from .input), Small/large, In a form (label + help, .field).
- State labels: centered on the box -> on the first line (Settings form Notes row).
- Corner (2026-11-19, review): --field-r from text-field. Default text area (3+ lines): calc(--field-r * --corner-k) + corner-shape squircle. .short (1-2 lines): round, capsule at one line. Size rows now 3 lines (2-line lg is under 2 r k). Inline padding from .input. "One line" row uses .short.
- Same as text field (owned by text-field / ds): edge, fill, focus ring, caret, selection, placeholder, no hover.

## Open
- Focus ring tokens: pending ds decision; Apple's ring is about 3.5 pt, 2 pt outside and 1.5 pt over the edge.
- 14 px text zooms the page on iOS Safari focus (needs 16 px on touch); same for the text field. For the mobile pass.
