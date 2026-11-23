# Timeline
One section per lane, one line per step, no subtext. "← you are here" in every section, on the first step that is not done (class `at`); that row has a fill when it is a wait step. Right column optional: date, cost or another value.

Matched the project dashboard the reviewer likes: lane sections with done/total, solid rail through the past, dashed into the future after the marked step, small gray dates on the right. Apple System Settings: mixed-case bold section headers, one-line rows, the value right-aligned in secondary gray. Finder sidebar: section header bold 11 secondary. 24 px rows (13 px text), 14 px between sections.

## CSS notes
timeline: one section per lane, one line per step, "← you are here" on the first step not done in each section.
Rail on the left: solid ink through the past, dashed gray into the future. Dots: done = ink, now = ink with a ring,
next = ink ring, wait = gray ring. No blue: blue is for "needs you". The value column (date, cost) is optional.

## Choices
- Chosen: The marker is on the step itself, the first one not done, with a fill when it waits. Other option: A pointer row between the last done step and the next one. (The row pointed before the step the project waits on; a waiting project should show that step at a glance.)
- Chosen: The pointer and the dots are ink (current step: ink dot with a ring). Other option: Blue pointer and blue current dot, as in the old dashboard. (Blue is only for "needs you" (2026-11-18 direction). Ink semibold still reads first in a gray list.)
- Chosen: Section headers mixed case, bold 11 px gray, done/total on the right. Other option: Uppercase letter-spaced headers, as in the old dashboard. (Same as every other section header in the system (Finder, Settings, Slack, our sidebar).)
- Chosen: Waiting steps: gray ring, then "waiting on X" on the same line in small gray. Other option: "Waiting" in the value column and X dropped. (Who we wait on is the useful part, and it stays one line (no subtext). Truncates with an ellipsis when narrow.)
- Chosen: Value column: 12 px gray tabular numbers; the project picks date, cost or nothing. Other option: Dates always shown.
- Chosen: Every lane shows all its steps (no collapsing of old done steps). Other option: Show the last 3 done steps per lane with a "N earlier" row. (The dashboard he likes shows them all; one line each keeps it short enough.)

## Measured
## Round 1: restore the project dashboard format
- Done: sections per lane (done/total), one line per row, no subtext, "← you are here" per section, date optional and replaceable by cost or another value (demo rows: Cost, No value, Narrow).
- Builder: `timeline(lanes, rows)` exported from demo.js. rows: {lane, state done|now|wait|next, label, on?, value?}. Future = from the first wait/next row of the lane (helpers drops the {here} row of timeline.json).

## Detail list (old dashboard -> ours)
- Row: 13 px callout, 24 px tall (old 13/1.5 + 6 = 25.5). Text starts 20 px from the rail column (old 22).
- Rail: 1 px solid ink past (old 1.2), dashed 3/3 gray future (old dashed border). Dash phase kept across rows (24 is a multiple of 6).
- Dots 8 px: done ink, now ink + 2 px bg gap + 1 px ink ring (old blue), next ink ring 1.25, wait gray (text-2) ring 1.25 (old dashed ring, blurry at 8 px).
- Pointer: on the first step not done (`li.at`): its label ink semibold, then "← you are here" in 11 px gray. A wait step there gets the fill background and a waiting-color ring, so a waiting project shows what it waits on at a glance.
- Section header: caption 600 11 px text-2, mixed case; count 400 text-3 tabular (old uppercase .08em ink).
- Value: 12 px text-3 tabular, never wraps; label truncates with an ellipsis.
- Panel title: "Timeline" + "updated N min ago" on the right (as old).

## More CSS notes
- rail: one segment per row, cut at the first and last dot centers; dashes are 3 on 3, so 24 px rows keep the phase
