# Card
One cue: a surface fill on the grouped background. No border, no shadow.

Matched Settings grouped cards: fill contrast only, 12 pt continuous corners. Removed the hairline and the drop shadow.

## CSS notes
Card: one cue only (Apple grouped style): a surface fill on the grouped background. No border, no shadow.
On a surface background use .card.on-surface (fill surface-2). Floating things (menus, popovers, sheets) are not cards.
Interactive: hover darkens the fill, pressed scales 98%, .selected = a darker gray fill (no ring: Apple selects with a fill), focus = the focus ring.

- .panel: a dashboard section. h2 is Title 2 with a gray count in small; small.end puts a note (updated ...) at the right.

- Board (polish 2026-11-20): a project dashboard as full-bleed sections split by hairlines, no card per section. Wide: Timeline over Needs you left (3fr/2fr), Done right; under 720 px one column, Needs you first. Ref: the live Admin Work dashboard and items/project-view.
