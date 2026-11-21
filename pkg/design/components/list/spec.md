# List
No hover fill. Selection is the accent fill, gray when the list is not focused. Fills that touch merge into one shape.

Matched macOS 26 (Finder sidebar and list, System Settings, Messages, all checked with a real pointer on a Mac): rows have no hover fill at all, so a hovered row never sits next to the selection with a notch between two rounded corners. Contiguous fills merge: the shared corners go square and a faint line divides them (Finder multiple selection). Press fills at once, no transition. Rows inset 8 from the list edge, 8 pt corners, hairlines inset to the text and hidden on both edges of a filled row. Slack instead fills on hover with 6 px corners on flush 28 px rows, which leaves the notch a reviewer saw.

## CSS notes
List and row. .list (plain), .list.separated (hairlines inset to the text), .list.grouped (Settings card of rows), .list.compact (28 px one-line rows).
Row: <div class="row" role="option">[lead: icon/avatar/dot] <span class="row-main"><b>Title</b><small>Caption</small></span> [trail: .row-trail time, badge, chevron]</div>
Apple (Finder, Mail, Messages, System Settings): no hover fill. Selection = accent fill, white text; gray when the list is not focused (.list.inactive).
Fills that touch merge into one shape: the touching corners go square (contiguous selection, or a pressed row next to the selection).

## Choices
- Chosen: No hover fill on list rows (Apple). The row reacts on press and selects. Other option: Keep a hover fill and square the corners that touch the selection, so hover and selection read as one shape. (Every Apple list checked (Finder, Mail, Messages, System Settings) shows nothing on hover. It removes the notch and is the simplest. Slack's hover fill is the case that makes the notch.)
- Chosen: Touching fills merge (pressed next to selected, two selected): shared corners go square. Other option: Leave a 2 px gap between rows so each rounded fill stands alone. (Finder does this for a multiple selection. A gap makes the list look like a stack of buttons.)
- Chosen: Selection stays the accent fill; it goes gray when focus is elsewhere (.list.inactive). Other option: Gray or ink selection always, to keep blue for needs you. (Apple's key list is accent; the gray state comes for free most of the time because focus sits in the composer, so blue is rare.)

## Measured
## Round 1
- Apple (Finder sidebar and list, System Settings, Messages; real pointer on a Mac): no hover fill at all. Contiguous fills merge into one shape with square shared corners and a faint line between (Finder multiple selection).
- Slack: hover fill (8% white), radius 6, flush 28 px rows: the notch a reviewer saw. That is what we had.

## Detail list (ref -> was -> now)
- Hover: Apple none -> gray fill -> none (also on .list.side, the sidebar uses .list).
- Touching fills: Finder merges -> separate rounded fills -> shared corners square (selected+selected, pressed next to selected), faint line between two selected rows (white 22% on accent, separator when inactive).
- Inactive selection: Messages/Mail gray when the list is not key -> none -> .list.inactive: fill-pressed gray, normal text, blue dot stays blue.
- Row inset: Apple 10, Slack 8 -> 6 -> 8.
- Focus ring: ring offset -1 -> drawn fully inside the row (offset = -focus-width) so it never overlaps neighbours; width and color stay ds tokens (pending decision).
- Press: instant fill, no transition (Apple) -> kept.
- Separators: hidden on both edges of a filled row, now also of a pressed row.
- Demo: was 3 rows, no states -> Rest, hover/pressed below the selection, two selected, focused, not focused, disabled, avatar+time (Mail/Messages), compact 28 (Slack density), grouped pressed (Settings).
- Added .row-trail (time/value, tabular numbers), compact and grouped body-size titles.

## Open
- None. Choices in item.json (hover none vs merged hover; merge vs gap; accent vs gray selection).

## More CSS notes
- Merge: a filled row next to a filled row loses the corners they share. Contiguous selection gets a faint line between rows (Finder).
- Selection gray (Messages when focus is in the composer); press a little lighter, so press then select reads as one step.
