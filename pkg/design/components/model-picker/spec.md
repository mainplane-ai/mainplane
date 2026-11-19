# Model picker
Pick the model for new sessions. Same list as the TUI's /model: connected providers only, in the catalog's order, one section per provider, then Custom for your own servers. Type to filter, arrows and Enter, check on the current model, its default effort on the right.

Matched the TUI (pkg/connector/tui, main 6ad8902): sections and order from connected() over the self-hosted-all group, catalog preset and provider names, default effort per preset as /config finds it (the settings[effort] value the preset's params hold: Medium, High, Minimal), Custom rows as the bare model id, filter on name, hint and section. Cursor's model picker for the search row on top (no field box, hairline under), Claude's for the Effort row with its value and a chevron in the footer, Slack's search for bold matches. Apple for the check column, inset concentric highlight and the plain pop-up that opens it. Solid floating fill, radius 12, 28 px rows, gray highlight.

## CSS notes
Model picker: a popover opened from the plain pop-up in Project settings. Search on top, models in provider sections
(the TUI's /model order), check on the current model, default effort on the right, Effort and Connect a provider in a footer.
<button class="select plain model-trigger" aria-haspopup="dialog" aria-expanded>Claude Opus 5.5<span class="pk-effort">High</span></button>
<div class="model-picker" role="dialog">
<label class="pk-search"><mp-icon name="search"></mp-icon><input role="combobox" placeholder="Search models"></label>
<div class="pk-list" role="listbox"><div role="group"><div class="pk-label">Claude API</div>
<div role="option" class="pk-row" aria-selected="true"><span class="pk-name">Claude Opus 5.5</span><span class="pk-hint">Medium</span></div></div></div>
<div class="pk-foot"><div role="option" class="pk-row pk-more" aria-haspopup="listbox"><span class="pk-name">Effort</span><span class="pk-hint">High</span><mp-icon class="pk-chev"></mp-icon></div></div>
</div>
One highlight (pointer or keys, .is-active) in gray: blue is kept for needs-you, as in ChatGPT, Claude and Cursor.
Solid floating fill, hairline edge, shadow-3, radius 12 with 6 px inset rows (concentric, as the context menu). model-picker.js: filter, keys.

## Choices
- Chosen: Gray highlight for the pointer and keyboard row (--color-fill), ink text. Other option: The accent highlight with white text of the context menu (macOS menus). (Blue means needs you. ChatGPT, Claude, Cursor and Slack search all use a gray row in their pickers. A long list with a blue bar under the pointer is loud. The context menu still uses accent: one of the two should change so menus match.)
- Chosen: Only connected providers are listed, as the TUI does. A footer row Connect a provider… opens the providers list. Nothing is shown grayed out for a missing key. Other option: List every catalog provider and gray out the ones with no key ("Add key" on the row). (The TUI hides them, and 19 providers with 90 presets, most disabled, would bury the few you can use. Cursor and Claude put the rest behind one footer row.)
- Chosen: Catalog order and sections are kept while filtering; matching rows stay in their provider section, the match in bold, the first match active. Other option: The TUI's order while filtering: prefix matches first, then the rest, which can split a provider into two sections. (The same model name exists under several providers (Claude Opus 5.5 under Claude API and Claude subscription): the section label is what tells them apart, so it must stay with its rows.)
- Chosen: Effort is a footer row (Effort  High ›) that opens a small list of the current preset's values (low … max), as Claude does. Other option: Effort as its own pop-up in the sheet, as the TUI's separate /config menu. (Effort belongs to the chosen model (the values differ per preset), and Claude's picker shows it is found there. The sheet stays one row.)
- Chosen: Check on the left in a 16 px column, the preset's default effort right-aligned in gray (Medium, High, Minimal). Custom models show nothing there. Other option: The context limit on the right, as the TUI's /model hint.
- Chosen: The picker opens below the pop-up, right edges aligned, scrolled so the current model is in the middle, focus in the search field. Other option: Apple's pop-up menu over the button with the current row on top of it. (With a search field on top and a list that scrolls, an over-the-button menu would put the search out of place. Cursor and Claude open next to their button too.)
- Chosen: The trigger shows the model name and the effort in gray (Claude Opus 5.5 High), not the provider. Other option: Name and provider (Claude Opus 5.5 · Claude subscription). (The row is narrow; Claude's composer shows name and effort the same way. The provider shows in the open picker as the section of the checked row.)
- Chosen: Custom servers that are still listing, or not answering, show a gray row with that state in the Custom section. Other option: The TUI's way: wait for every server, then open, and report errors in the transcript. (A web popover should open at once. A slow local server should not delay the whole menu, and its error belongs next to it.)

## Measured
Listed in tools/items.mjs under Controls after popup-button.

## Source of truth: the TUI (mainplane-repo main 6ad8902)
- commands.go models(): connected(cat, ps) = presets of catalog group self-hosted-all whose provider the harness serves, in group order. Section = cat.Providers[p.Provider].Name, label = preset Name, hint = tokens(ContextLimit) ("current · " prefix on the current preset).
- Then Custom: customItems() = each openai-chat provider the catalog lacks (local/keyless servers), its server's model ids, hint "<provider> · <limit>" (limit = provider's or catalog.CustomLimit 30000 -> 30k). A server that fails gives an error, no rows.
- Unconnected providers are not listed (/providers lists those, sections Subscriptions, Frontier labs, Clouds, Gateways, More).
- menu.go filter(): label/hint prefix matches first, then contains over label+hint+section. Section title rows, a blank row between sections.
- Effort: not in /model; /config shows the preset's settings (Effort section, single check: none/minimal/low/medium/high/xhigh/max per preset).
- Group order in the catalog: anthropic(Claude API), openai, google, meta, xai, deepseek, moonshot, zai, minimax, xiaomi, mistral, bedrock, openrouter, chatgpt(ChatGPT subscription), opencode-go*(all "OpenCode Go", so one section), claude-code(Claude subscription). Subscriptions come last in /model.

## Detail list (refs -> ours)
- Panel: Cursor/Claude white, radius ~12, 1 px edge, soft shadow. Ours --color-floating, hairline edge, shadow-3, radius 12 squircle, 300 px wide. Done.
- Search: Cursor row on top, icon + placeholder, no box, hairline under. Ours 40 px, 16 px icon on the check column, caret accent, no ring. Done.
- Rows: Cursor 30 pt/13 pt, Claude 2-line. Ours 28 px/14 px (context menu family), inset 6, radius 6. Done.
- Highlight: all refs gray. Ours --color-fill, hint turns text-2. Done (choice: not accent).
- Check: ChatGPT/Claude right; Apple left. Ours left 16 px column. Done.
- Hint: the preset's default effort, right, text-3 (Medium/High/Minimal = the settings[effort] value whose params the preset's params hold, as /config's holds()). Custom models: empty. The Effort footer row shows the session's current value (High in the demo, raised from Opus 5.5's default Medium). Done.
- Sections: label 13 text-2 on the label column, 8 px gap between groups, no lines, sticky while scrolling. Done.
- Filter: matches bold (Slack), sections kept, first match active, "No models match “q”". Done.
- Footer: Claude's Effort row (value + chevron) and Cursor's More Models -> Connect a provider…. Done.
- Effort submenu: 132 px, first row level with the Effort row, overlap 3 px, check on current. Labels: xhigh -> Extra high. Done.
- Open: below the trigger, right edges aligned, 4 px gap, fade+4 px drop (--duration-fast), current model centered, focus in search. Done.
- Pick: 70 ms blink, check moves, trigger updates, closes, focus back on trigger. Esc: clears query, then closes. Done (tested with real input events).
- Custom: server listing / not answering as gray disabled rows. No provider: text + Connect button, no search. Done.
- Trigger: .select.plain button, name + effort in gray; .is-open keeps the pressed dot. Done.

## Open
- The context menu highlights in accent; this picker in gray. One should change so menus match (in item.json choices).

## More CSS notes
- Search: Cursor's row. Icon on the label column, no field box, no ring (the open picker is the focus context); hairline under.
- List: scrolls past 12 rows. Sections: a gray label on the label column, 8 px above each one after the first; no lines.
- Trigger: the plain pop-up with the effort in gray after the name (Claude's composer shows "Sonnet 5.5 Medium").
