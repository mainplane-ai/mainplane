# Card
One cue: a surface fill on the grouped background. No border, no shadow.

Matched Settings grouped cards: fill contrast only, 12 pt continuous corners. Removed the hairline and the drop shadow.

## CSS notes
Card: one cue only (Apple grouped style): a surface fill on the grouped background. No border, no shadow.
On a surface background use .card.on-surface (fill surface-2). Floating things (menus, popovers, sheets) are not cards.
Interactive: hover darkens the fill, pressed scales 98%, .selected = a darker gray fill (no ring: Apple selects with a fill), focus = the focus ring.
