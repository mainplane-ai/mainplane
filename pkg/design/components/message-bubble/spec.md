# Message bubble
Sent is blue with white text, received is gray. Grouped bubbles tuck their corners; the last one has the tail.

Matched iMessage: blue sent bubbles with white text, 18 pt bubbles, 6 pt tucked corners, tail on the last, emoji-only without a bubble, Delivered note, day separator, blue tapback for your own reaction.

## CSS notes
Conversation (iMessage shapes, brand colors). .thread holds .msg rows; .msg.sent is right-aligned.
Sent = blue bubble with white text (--color-bubble-sent, as in iMessage), received = --color-bubble-received, system = centered
caption. Grouped bubbles shrink the corner between them to 6; the last bubble of a group (.tail) gets the iMessage tail.

## Choices
- Chosen: Sent bubbles are blue #0087A4 (--color-bubble-sent, dark #0B87A3) with white text, as in iMessage. Blue now means needs you AND your own messages. Other option: Ink sent bubbles (paper in dark), blue only for needs you. (Your own messages read at a glance as yours, like iMessage. White on #0087A4 is 4.2:1, under AA 4.5 for 15 px text; Increase Contrast should darken the fill (asked ds for #00748E, 5.4:1).)
- Chosen: Bubble text 15/22 (body large), 7/12/8 padding, 18 radius. Other option: iMessage's 13/16 text. (A web chat reads at Slack's 15 px; the shapes (radius, tucked 6 px corners, tail) follow iMessage.)
- Chosen: Agent replies with markdown blocks leave the bubble (see Rich text); inline formatting stays inside it. Other option: Everything in bubbles. (Tables and code do not fit a 72% bubble; ChatGPT and Claude do the same.)
- Chosen: On blue: inline code chip is white 20%, links underline in white 40%, text selection white 32%; the reply quote stays the gray outline of the quoted (received) message above the blue bubble. Other option: Paper-tinted chips; a blue-tinted quote. (iMessage keeps the quoted bubble in its own color; white tints stay readable on blue in light and dark.)

## Measured
## Feedback round 1
- Thread: looks good; replies and tapbacks stay modeled, no work (orchestrator).
- Rich content: markdown, code, LaTeX wanted. Done as a new item items/rich-text/ (ds added it to tools/items.mjs after message-bubble).

- Layout: block reply = unbubbled column (.msg.rt-msg > .rt), text aligned with bubble text (12 px inset), max 640 px, 20 px under the question. Inline-only reply = bubble with .rt-inline.
- Body 15/22 (Slack 15/22.5, Claude ~15). Paragraph gap 12. Headings # 18, ## 16, ### 15 semibold, 20 above 6 below (Claude-quiet, not ChatGPT-large).
- Lists: 24 indent + 2 (Slack 28), 4 px item gap, markers text-2, nested ul circle.
- Quote: 3 px line bar, 14 px inset, text-2 (Claude faint bar, Slack 16 inset).
- Inline code: mono .87em, fill chip radius 4, padding 2 5, nowrap; on sent ink bubble paper 18%.
- Code block: surface-2, radius 12, 32 px header (language, Copy on hover/focus, always on touch), 13/20 mono, 14 px side padding, scrolls x.
- Syntax: --syn-k/s/n/t item vars (light plum/green/amber/violet, dark lighter), comments text-3 italic, shell output text-2.
- Table: hairlines, header semibold with line color, content width, .num right + tabular, total row semibold, scrolls x.
- Math: Temml at build time (items/rich-text/temml.mjs, v0.11.11) to MathML; font STIX Two Math / Cambria Math; display centered 1.12em, scrolls x. Trailing punctuation kept with the math (.nobr).
- Links: ink, 40% underline, solid on hover.

## Open

## Blue sent bubble (2026-11-19)
- Sent: --color-bubble-sent / -ink (#0087A4 / #0B87A3 dark, white). Received: --color-bubble-received / -ink (matches typing indicator). Own tapback (.tapback.mine) blue, shown in demo. Selection on blue: white 32%. rich-text inline code on blue: white 20%.
- Quote stays the gray outline of the quoted received message (iMessage).
