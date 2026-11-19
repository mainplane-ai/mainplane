# Scrollbar
Every scrolling box and the page use it: add class "scroller" (overlay, shows while scrolling) or "scroller always" (always shown). No bar at rest: content is the interface; the bar appears only while it says something.

Matched Messages and Finder on macOS 26 (measured at 2x): a thin overlay thumb, 6 px (Messages 7 pt, Finder 6 pt), 3 px from the edge and the ends, full round, text color at 55% (Apple #7F7F7F on white, white .55 on dark). Fades in in 0.1 s on scroll, holds 0.8 s after the last scroll, fades out in 0.3 s. The pointer on a shown bar keeps it and darkens it to 65%; a hidden bar stays hidden under the pointer, as in Messages. It does not grow (Messages and Finder grow to 11 pt over a track). Always: the same 6 px thumb on a 6% rail. Increased contrast: 75% / 85% thumb, 12% rail. Not Slack's 8 px bar that shows on hover of the whole list.

## CSS notes
Scrollbar: thin macOS-style scrollers for any scrolling box (html, a list, a panel).
.scroller          overlay: no bar at rest; a 6 px thumb while scrolling that fades 0.8 s after the last scroll.
The pointer on a shown bar (or dragging it) keeps it and darkens it; it never grows and has no track.
A hidden bar stays hidden under the pointer, like Messages. scrollbar.js sets data-sb="on|bar".
.scroller.always   always shown: the same 6 px thumb on a 6 px rail.
Geometry: 17 px bar (all of it is the drag target), thumb 6 px, 3 px from the edge and the ends, full round.
Chrome and Safari draw ::-webkit-scrollbar; Firefox gets scrollbar-width/scrollbar-color (Chrome 121+ ignores
::-webkit-scrollbar once those are set, so they stay behind @supports). The bar takes 17 px of layout on the web.

## Choices
- Chosen: Overlay by default: no bar at rest, a 6 px thumb while scrolling that fades 0.8 s after the last scroll. On the bar it stays and darkens; it does not grow and shows no track. Other option: Messages and Finder: grow to 11 px over a light track when the pointer is on the bar (our round 1). Or Slack: show the bar whenever the pointer is over the list.
- Chosen: Always: the same 6 px thumb on a faint 6 px rail. Other option: macOS legacy Always scroller: 11 px thumb in an 11 px track. (Same weight as the overlay bar, so turning Always on changes only whether it shows. The rail tells the length of the content.)
- Chosen: The web bar reserves a 17 px gutter on the right (thumb drawn in it), in both variants. Other option: A true overlay drawn over the content with JS (Slack's c-scrollbar), no gutter. (Browsers only let CSS style a bar that takes layout space. A JS-drawn bar needs drag, wheel, resize and keyboard handling and can lag the content. 17 px is what macOS itself reserves with Always on; boxes keep their content padding, so the gutter reads as margin.)
- Chosen: Thumb and rail are the text color mixed down (55% thumb, 65% under the pointer, 6% rail), so they tint with the ink theme. Other option: Apple's neutral gray (black or white at 50%). (Differs from Apple by a hair (#7F8385 vs #808080) and stays in the ink/paper palette in both modes.)
- Chosen: Our bar everywhere, including Chrome on Windows and macOS (it replaces their native bars). Other option: Keep the native bar where the system already draws an overlay one (Safari and Chrome on a Mac with a trackpad). (One look on every machine; Windows 11 Chrome's overlay bar is a different shape and color. The cost is the 17 px gutter on Macs that would overlay.)

## Measured
## API
- .scroller on any scrolling box or <html>: overlay bar. .scroller.always: always shown. scrollbar.js (global) sets data-sb="on" while scrolling (cleared 0.8 s after the last scroll) and "bar" while the pointer is on a SHOWN bar or drags it (a hidden bar is never revealed by hover, like Messages).
- Fixture only: .sb-static (no real scrolling) + <i class="sb-draw [h]" style="--t:top%;--h:len%"> draws the bar. The check browser runs with --hide-scrollbars, so shots never show real bars.

## Spec (round 2)
- 17 px gutter (whole column is the drag target); thumb 6 px, 3 px from the edge and ends, full round. Never grows, no track in overlay.
- Thumb text color 55%, 65% with the pointer on it (.15 s). Always: same 6 px thumb on a 6% rail. Contrast more: 75/85%, rail 12%.
- Fade: in .1 s, hold .8 s (JS), out .3 s. Registered custom props --sb-a/--sb-x (inherits: true, scrollbar pseudos see only inherited values).
- Verified in real Chrome 157 on a Mac at 2x: 12 device px thumb scrolling and on hover, hover at the edge of a hidden bar shows nothing, Always rail 12 px.
- Messages measured: 7 pt scrolling, 11 pt on the shown bar. Round 1 had 6 -> 11 px + track, shown by hover alone (likely the "thick" bars the reviewer saw when crossing panes).

## Open
- No small size; add .scroller.sm if text areas or composers adopt it.
- Candidates to adopt .scroller (their owners): text-area (own scrollbar-width thin), composer textarea, sheet body, tool-call .code.

## More CSS notes
- Drawn bars for the fixture (the check browser hides real scrollbars): same geometry and colors. <div class="scroller sb-static" data-sb="on|bar"> content <i class="sb-draw" style="--t:top%;--h:height%"></i></div>
