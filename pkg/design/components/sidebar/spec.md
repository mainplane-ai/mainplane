# Sidebar
One project list, no section headers, sorted by status: needs you, idle, working, waiting, closed. No hover fill; selection is gray.

Matched Messages for Mac (macOS 26, measured on a Mac with a real pointer): rows inset from the panel edge, 10 pt corners, rows flush with no gap, 40 pt round avatar, a leading dot column (Messages' unread dot, here blue for needs you), name semibold with the time right-aligned on its baseline, preview two lines in secondary, hairlines inset to the text and hidden next to the selection. No hover fill at all (Messages, Finder, Settings), so the notch between a hovered and a selected row is gone. Fills that touch merge (from .list). Removed the Needs you / Waiting / Closed headers and counts.

## CSS notes
Sidebar: the project list (Messages for Mac, macOS 26). One list, no section headers, sorted by status: needs you, idle, working, waiting, closed.
<nav class="sidebar"><div class="top">label.search.lg + new button</div><div class="list side separated" role="listbox"><div class="row needs|idle|working|waiting|closed" role="option"><span class="u"></span>.avatar
<span class="row-main"><span class="h"><b>Name</b><span class="row-trail">Time</span></span><small>[.typing.inline when working] Preview</small></span></div></div></nav>
Rows build on .list: no hover fill, touching fills merge. Selection is gray, never the accent (blue is for needs you).

## Choices
- Chosen: No hover fill on sidebar rows. Press fills a little, then the row selects. Other option: Slack's hover fill, with square corners where it touches the selection. (Messages, Finder and System Settings show nothing on hover. It removes the gap the reviewer noticed and keeps the list calm.)
- Chosen: Messages rows: 40 pt avatar, name and time, two-line preview (80 px per row, insets measured on Messages). Other option: Slack compact rows: 28 px, one line, small icon. (Matches the sketch and the mock. The preview line shows what each project wants, which is the point of the list. Compact can come later as a density option.)
- Chosen: Status shown in the row, not by headers: blue dot = needs you, gray dot = waiting, typing dots = working, dimmed name = closed, nothing = idle. Other option: A status pill on each row, or small headers. (Messages' unread dot is the strongest quiet cue Apple has. Blue stays reserved for needs you; the sort order already groups the rest.)
- Chosen: Selection is the gray fill (Messages when focus is in the composer), never the accent. Other option: Accent selection while the list has focus, as .list does. (In the sidebar the focus nearly always sits in the composer, and an accent row would compete with the blue needs-you dots.)
- Chosen: Hairline separators inset to the text, hidden next to the selection. Other option: No separators (Slack). (Messages for Mac draws them; with two-line rows they help the eye find row edges without a hover fill.)
- Chosen: Working rows keep their status line: the typing dots lead it, inline before the gray text (••• Drafting the launch post). Other option: Dots after the text, or dots on their own line in place of the preview. (Leading dots read first, like Messages' typing bubble at the start of a line, and stay visible when a long status is cut to two lines; after the text they would be clipped by the ellipsis.)

## Measured
## Round 1 feedback: no sections, sort by status. Orchestrator: one list, needs you > idle > working > waiting > closed; Messages + Slack hover/selection.

## Measured (Messages for Mac, macOS 26, 2x px / 2 = pt)
- Sidebar panel 320 pt; rows inset 10 pt each side, 300 pt wide, 80.5 pt tall, flush (no gap).
- Selection: rounded rect ~10 pt; unemphasized #98a8d9 (focus elsewhere). Two selected rows do NOT merge: notch at the touch.
- No hover fill (pointer on a row changes nothing). Finder, Settings: same (list agent).
- Avatar 40 pt, left 18 pt from row; text at 64 pt; name 13 pt bold, time regular secondary on the name line, preview 2 lines secondary reserved.
- Hairline separator under each row, inset to the text, darker over selection.
- Slack: hover fill on flush 28 px rows, 6 px corners: notch next to selection (what the reviewer saw).

## Ours (done)
- Insets from project-view's Messages table): panel 320, padding 10 0 8; .top = search-field .lg (36) + ghost new button, 10 from the panel sides, 8 to the first row; list padding 0 2 + row margin 8 = 10.
- One .list.side.separated; rows padding 12 11 12 2, gap 6: dot column 10, avatar 40 at 18 from the row edge, 6 to the text, 11 at the right, 80 tall (working row too). Name 14/600 + time 12, preview 13/18 two lines (min 36). 10 px corners.
- Status cue in the dot column: blue = needs you, gray 8 px = waiting; typing dots = working (inline before its status line, round 2); closed = text-2 name, dim avatar; idle = nothing.
- No hover fill. Press = fill-hover; selected = fill-pressed (gray, never accent). Touching fills merge (list.css).
- Separators inset 64 px, hidden next to selection/press (list.css).

## Open
- project-view can drop its .app > nav overrides (width, padding, .top, list padding, sep-inset, row padding/gap); it keeps only margin 8 and flex/overflow for the window layout.
