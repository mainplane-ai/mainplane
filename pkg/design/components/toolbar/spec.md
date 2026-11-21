# Toolbar
The 52 pt title band of a project. Opaque: content never shows under the title. Star, avatar, title over subtitle on the left; ghost rounded-rect buttons on the right.

Matched Slack channel header for layout and buttons (star first, 28 px ghost buttons, radius 8, 6 px apart, 5% ink hover fill); macOS 26 for the 52 pt height, headline title over footnote subtitle and the soft scroll edge that appears only once content is under the band.

## CSS notes
Toolbar: the 52 pt title band of a project (macOS 26 window toolbar height, Slack channel header layout).
Left: star toggle, avatar, title (headline) over subtitle (footnote). Right: ghost buttons, 28 pt rounded rects
(icon-button's .btn.icon.ghost, Slack's 8 px hover fill), 6 pt apart as Slack's header. The band is opaque: content never shows
under the title. .scrolled adds Apple's soft scroll edge below the band (bg fading out over 16 pt), no hairline.

## Choices
- Chosen: Star toggle: outline when off, filled when on, one click (Slack's classic channel star). Other option: Slack today: the star opens a 'Move to...' menu (Starred / sections). (One project list with no sections to move to; a direct toggle is one click and shows its state in the glyph.)
- Chosen: Filled star in ink. Other option: Slack's gold star (#AA8000) with a yellow hover fill. (The palette keeps colour for meaning: blue is needs you, red destructive. Gold would be a new colour with no meaning.)
- Chosen: Ghost buttons straight on the band, rounded rect 28 px, radius 8, 6 px apart (Slack). Other option: macOS 26 solid capsule groups (Finder, Notes).
- Chosen: Opaque band; the soft 16 px scroll edge shows below it only while content is scrolled under. Other option: Translucent band with content fading under the title (macOS 26 scroll edge). (Content under the title read as overlap. Opaque keeps the title clean; the fade below still tells that more is above.)
- Chosen: 52 pt height (macOS 26 toolbar). Other option: Slack 49 px. (Room for title + subtitle; matches Apple first.)
- Chosen: Agents button: people glyph + count in one ghost rect, like Slack's member count. Other option: An avatar stack (Slack shows faces). (Agents have no faces; a count is what matters.)

## Measured
## Round 1 feedback
- Pin -> star toggle: outline off, filled ink on. Done (.btn.icon.ghost.star, aria-pressed; filled by path fill).
- Content overlapped the title: band is now opaque (--color-bg); soft 16 px edge below it only in .scrolled. Done.
- Circular icon buttons -> Slack rounded rects: icon-button now draws rects (--ib-r 8 at 28); toolbar dropped its capsule groups and own hover. Done.

## Detail list (Slack/Apple -> ours)
- Height: Slack 49, macOS 52 -> 52.
- Buttons: Slack 28x28 r8, 6-7 px apart, hover 5-6% ink -> 28 r8 (squircle), 6 px, --color-fill 7%.
- Star: Slack 28 button, first item, 4 px to name -> first item, 6 px to avatar. Filled colour gold -> ink (choice).
- Member count: Slack 40x28 ghost with count -> people glyph + count, ghost rect, 7 px side padding.
- Glyph rest colour: Slack #454447 -> 80% ink (icon-button). Hover full ink.
- Title: 14/600 over 11 footnote, both ellipsize (Narrow row).
- No divider under the band (Slack has none in 4.53; Apple none).

## Asked of others

## More CSS notes
- star: Slack's channel star. Off = outline, on = filled ink. A toggle shows its state by the glyph, so no selected fill.
