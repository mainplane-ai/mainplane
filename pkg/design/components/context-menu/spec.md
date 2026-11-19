# Context menu
Solid floating panel. One highlight (pointer or keys) in gray (as the model picker; blue means needs you), red only for destructive rows. Opens at once; the chosen row blinks, then the menu fades.

Matched Slack first: 28 px rows, 14 px text, full-width section lines, gray section labels, gray shortcuts, red destructive row with red hover fill. Apple where Slack is worse: inset rounded highlight concentric with the 12 px panel, one label column for icon-less and checked rows, check column, compact ⌘ shortcuts, submenu level with its row, outline on the right-clicked row.

## CSS notes
Menu and context menu. Slack is the lead reference (28 px rows, full-width section lines, section labels, red destructive
row); gray highlight, as the model picker (blue is kept for needs you); Apple where Slack is worse (inset concentric highlight, one label column, check
column, compact shortcut glyphs, submenu aligned to its row, instant open, blink on choose, outline on the target).
<div class="menu" role="menu">
<button role="menuitem"><mp-icon name="copy"></mp-icon><span>Copy</span><kbd>⌘C</kbd></button>
<hr><div class="menu-label">Notify me about</div>
<button role="menuitemradio" aria-checked="true"><span>All messages</span></button>
<button role="menuitem" aria-haspopup="menu"><span>Move to</span><mp-icon name="chevron-right" class="chev"></mp-icon></button>
<button role="menuitem" class="danger">...</button>
</div>
One highlight at a time: hover moves focus (context-menu.js), so :focus is the highlight. .is-hover forces it in demos.
.menu-target: outline on the thing the menu was opened on (Finder).

## Choices
- Chosen: Gray highlight for the pointer and keyboard row (--color-fill), ink text; icons keep text-2, shortcuts go text-3 to text-2. Destructive rows keep the red fill with white. Other option: Accent fill with white text, icons and shortcuts (macOS menus, Slack). (Blue means needs you. Every menu matches the model picker, as ChatGPT, Claude and Cursor use gray rows.)
- Chosen: The right-clicked row gets a 2 px accent outline while its menu is open, as Finder draws it. Other option: No outline: the menu alone shows which row it acts on. (Apple does it (menus/finder-context-menu-light.png). It is the only colored ring that is not keyboard focus.)

## Measured
## Detail list (Slack / Apple / ours)
- Panel: Slack white, radius 4, 1 px ring 13% + 0 4px 12px, 12 px top/bottom pad, 300 px fixed. Apple solid under RT, radius ~13 pt, 5 pt inset, content width. Ours: --color-floating, radius 12 (squircle k), 6 px pad all round (concentric with the 6 px row radius), min 220 / max 320, one hairline edge + shadow-3.
- Rows: Slack 28 px, 15 px text, 24 px side pad. Apple 24 pt, 13 pt. Ours 28 px (Slack), 14 px body token, 6 + 10 px to the icon.
- Highlight: Slack full-bleed square #1264A3 bar (flaw: square bar inside a rounded panel, touches the edges). Apple inset rounded accent. Ours: Apple shape, gray --color-fill (orchestrator 2026-11-19: menus match the model picker, blue is for needs you), ink text, icons stay text-2, shortcuts text-2. Hover and keyboard share one highlight (hover moves focus), leaving the menu clears it.
- Lines: Slack full width, 8 px above and below. Apple inset. Ours: Slack (full width, hairline, 6 px above and below): it splits groups more clearly.
- Section label: Slack "Notify you about…" 13 px gray. Ours .menu-label, callout 13 px, text-2, on the label column.
- Checks: Slack hangs the check left of the icon and indents only checked rows (flaw: three label columns). Apple: check column. Ours: Apple, a 16 px check column; any icon or check in a menu gives every row the same label column.
- Shortcuts: Slack mixes words and symbols ("⌘ Opt Click", flaw). Apple compact glyphs. Ours: compact glyphs (⇧⌘L), text-3, same size as the label.
- Submenu: Slack gray chevron; submenu top level with the parent menu top (flaw: far from the row). Ours: chevron text-2; submenu overlaps 3 px, first row level with the parent row, parent row stays highlighted, 150 ms grace when crossing rows toward it, ArrowRight/Left.
- Destructive: Slack red text, hot red fill on hover. Apple has none in context menus. Ours: Slack, danger-ink text and icon, danger fill + white on hover.
- Disabled: text-3, never highlighted, skipped by arrows.
- Motion: Slack none at all. Apple: instant open, chosen row blinks once, menu fades out. Ours: Apple (no open scale; 70 ms blink; fade by --duration-fast; none under reduced motion).
- Target: Slack shows nothing for what the menu acts on. Finder outlines it. Ours: .menu-target 2 px accent outline while open (MPMenu.context sets it).
- Focus: a mouse open focuses the menu (no row highlighted, as Apple and Slack); a key open would start at the first row with ArrowDown.

## Behavior check (headless Chrome, real input events)
- Right click opens at the pointer, outline on target; ArrowDown/Right/Left/Enter; disabled skipped; submenu hover and grace; click on a submenu row runs its click and closes. No page errors.
