# Sheet
Project settings: title, close, grouped form. Hugs its content; changes apply at once.

Matched macOS 26 System Settings sheet (Users & Groups): 20 pt padding, 18 pt continuous corners, grouped box at 4% ink with 10 pt corners and no border, rows 40 pt with 10 pt insets, hairline inset 10 pt, 13 pt label with 12 pt gray help, plain pop-up on the right, gray footnote under the box. Slack modal for the header: bold title top left, X top right. Solid floating fill (no glass).

## CSS notes
Sheet: a modal card over the scrim (.scrim from alert.css). Header (title, optional subtitle, close X), body that hugs its content and scrolls past max height.
<div class="scrim"><div class="sheet" role="dialog" aria-modal="true" aria-labelledby=..><header><hgroup><h2>..</h2><p>..</p></hgroup><button class="btn icon ghost" aria-label="Close">..</button></header>
<div class="body"><div class="form"><label class="form-row"><span class="row-main"><b>Label</b><small>Help</small></span>control</label>..</div><p class="note">..</p></div></div></div>
Solid floating fill + edge + shadow (no glass). Radius 18 continuous (macOS 26 sheet). Changes apply at once: no Save/Done footer, Esc or X closes.
Optional .tabs under the header and <footer> for sheets that do need a confirm.

## Choices
- Chosen: Close with an X at the top right (and Esc). Changes apply at once, so there is no footer. Other option: Apple's footer with Cancel and Done/OK under a hairline (System Settings sheets). (Slack Preferences and channel details work this way. One pop-up does not need a confirm step, and the sheet stays small. The footer stays in the css for sheets that need a confirm.)
- Chosen: No tabs. One grouped box with one row: Orchestrator model. Other option: Tabs (Model, Workers, Apps, Description) as in round 1.
- Chosen: The sheet hugs its content (max height = stage minus 48 px, then the body scrolls). Other option: Fixed height, so the sheet does not jump between tabs. (macOS sheets size to their content. A fixed height left a large empty area under one setting.)
- Chosen: Plain pop-up on the right of the row (value in ink, chevrons in a small round box, from the pop-up button item), like System Settings. Other option: Bordered pop-up button under the label (round 1). (In a grouped form Apple puts the control on the row's right edge. The label and help text stay on the left.)
- Chosen: A gray note under the box: Workers get their models from the orchestrator. Ask it in the chat. Other option: No note. (It tells people where the other model choices went, so they do not look for them.)

## Measured
## Round 1 feedback
- "The actual content is a model picker for the orchestrator; worker model is decided in natural language. Sparse is fine." Done: one grouped row, Orchestrator model, plus a note that workers get models through the chat. Tabs and the "smaller model" checkbox removed.

## Refs
- Old: Slack channel details and Preferences modals, RefZoo sheet, iOS 27 zoo sheet.

## Detail list (Apple Users sheet -> ours)
- Sheet width 470 pt -> min(460px, 100% - 32px). Done.
- Padding 20 pt -> --pad 20px. Done.
- Corner: continuous ~18 -> --radius-panel (18 x corner-k squircle). Kept.
- Fill: white / #232532 -> --color-floating + edge + --shadow-4 (token, ds). Kept.
- Group box 3% ink, no border, corner ~10 -> 4% of --color-text via color-mix, --radius-10. Done.
- Row 36.5-40.5 pt, 10 pt inset -> min-height 40, padding 8/10. Done.
- Separator inset 10 pt both sides -> ::before left/right 10px, --color-separator. Done (only shows with 2+ rows).
- Label 13 pt, help 11 pt gray -> 13/18 400, help 12/16 text-2 (12 for web legibility). Done.
- Gap between boxes 10 pt -> .form + .form margin 10px. Done.
- Footnote under the box, gray, inset to the row text -> .note 12/16 text-2, margin 6px 10px. Done.
- Footer Cancel/OK under hairline -> not used (choice: X + instant apply). footer css kept for confirm sheets.
- Title: Apple sheets mostly have none; Slack has bold title top left with X -> title-3 + gray subtitle (project name) + ghost icon X, centers aligned. Done.
- Motion: macOS sheets fade/scale in -> mp-sheet: opacity + 8px rise + 2% scale, spring, distance x --motion. Done.
- Height: hugs content, max-height stage - 48, body scrolls. Done.

## Open
- None. Focus ring follows the shared token decision (ds).

## More CSS notes
- Grouped form (System Settings): rows in one rounded box, label left, control right, hairline between rows inset by the row padding.
