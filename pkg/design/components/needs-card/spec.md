# Needs you card
Questions with answer buttons. The only blue in a dashboard. Reply on any open question: an inline field; the message to the orchestrator starts with Replying to: and the title cut near 40 characters.

Our own (first project dashboard). Reply: Slack hover action (glyph + word, gray, fill on hover), Messages macOS 26 reply field (placeholder Reply, send circle only with text, quote pill above the sent bubble).

## CSS notes
Needs you card: questions with answer buttons, the only blue in a dashboard. Needs push-button's .btn and icon-button's .btn.icon.
Each open need has a Reply button at the end of its answer row (gray, plain until hover, like Slack's "Reply" hover action).
Reply opens an inline field under the answers (Messages macOS 26: placeholder "Reply", send circle only once there is text).
Return sends, Shift+Return is a new line, Esc closes. After a send the card shows "You replied" and stays open: the orchestrator removes it.

## Choices
- Chosen: Reply is always visible, trailing the answer row, gray glyph + word with no fill until hover (Slack's Reply action). Other option: Show Reply only on hover (Slack hides its actions until hover). (Dashboards are used on phones, where there is no hover, and a question card has few controls, so one quiet button costs little.)
- Chosen: Reply opens an inline field in the card, under the answers. Other option: Reply fills the chat composer with a quote chip (needs a new bridge call, e.g. draft(quote)). (The question stays in view while you write (Messages keeps the quoted bubble in view), the bridge keeps its single send(text), and the dashboard does not depend on the chat dock being on screen.)
- Chosen: The quote is the title cut at the last word break before 40 characters, then an ellipsis: Replying to: "Use the EV certificate for the Windows…". Other option: A hard cut at 40 characters mid-word ("...Windows b…"). (Reads as words, and the start of the title is still enough for the orchestrator to match its open question.)
- Chosen: After a send the card stays open with one line: You replied, then the text; the orchestrator removes or rewrites the card. Other option: Remove the card at once, or mark it answered. (A reply is not an answer. The orchestrator may come back with a follow-up; the user sees that the reply went out.)
- Chosen: Field: gray pill 32 px (composer style, smaller), placeholder Reply, send circle pops in only with text; Return sends, Shift+Return new line, Esc closes and keeps the draft. Other option: A bordered text area with Send and Cancel buttons (Slack thread box). (Apple Messages pattern and the same parts as our composer; fewer controls in a small card.)
- Chosen: Composer quote chip (for Reply on a chat message): first row inside the composer pill, gray surface-2 like the attachment chip, reply glyph, "Replying to" in gray, quote in ink, close x. In the chat the quote shows as the reply-quote pill above the sent bubble. Other option: A left gray bar quote (Slack blockquote / WhatsApp). (Same shape family as the attachment chip in the same field, and the sent message matches the Messages reply pill already in the message-bubble item. No blue: blue stays for the needs-you dot and primary answer.)
- Chosen: Reply glyph: the left-turning arrow (Mail and Messages Reply, SF arrowshape.turn.up.left). Other option: Slack's speech bubble glyph (its Reply means a thread). (Apple first; the arrow says reply to this, a bubble says open a thread.)

## Measured
## Files
- demo.js (fixture: live card, Reply states, open/typing, sent, composer chip, chat, wire text), needs-card.css, needs-card.js (live demo behavior), item.json.
- helpers.mjs needHTML (ds) has no Reply button yet, so project-view's mockup shows none. Markup to copy is in demo.js need().

## Bridge message (dashboard to orchestrator)
- The dashboard uses the existing bridge call send(text) (Agent Interface 5.3). No new call, no metadata.
- text = `Replying to: "<quote>"` + blank line + the user's text, trimmed. Example:
  ```
  Replying to: "Use the EV certificate for the Windows…"

  Wait until Microsoft closes the review, then sign both builds.
  ```
- quote = the need title (h3 text). If it is 40 chars or less, whole and with no ellipsis. Else cut at the last space at or before char 40 (if that leaves at least 24 chars, else a hard cut at 40), strip trailing space and punctuation, add "…" (U+2026). Straight double quotes around it.
- The chat UI shows a user message whose first line matches `^Replying to: "(.+)"$` followed by a blank line as the reply-quote pill (.quote of message-bubble) over the bubble, with the rest as the bubble text. The orchestrator sees plain text.
- Answer buttons are unchanged (they send their own message as before).
- The composer chip (Reply on a chat message in the parent UI) builds the same text with the same cut from the replied-to message's first line.

## Detail list (refs vs ours)
- Reply button: Slack = glyph + word, 13 px bold gray (#5E5D60), no fill until hover, in a floating bar. Ours: .btn.sm 24 px, 13/500, text-2, transparent until hover (fill), text on hover/press, padding 8, trailing in the answer row (margin-left auto). Open = selected fill (fill-hover) + ink, aria-expanded. Glyph: arrow (Apple Mail/Messages), not Slack's bubble.
- Field: Messages = plain pill, placeholder "Reply", send only with text. Slack = bordered box with toolbar. Ours: fill pill radius 16, min 32 px, body 14/20, padding 6/12, send circle 24 accent filled inside (4 px inset), scale .5 to 1 with spring 150 ms when text appears. Max 120 px then scroll. Focus: composer ring (box-shadow focus-width), follows ds's pending focus-ring decision.
- Open motion: grid rows 0fr to 1fr 250 ms ease-out with fade 150 ms; field indented 16 like the body text.
- Keys: Return send, Shift+Return newline, Esc close (draft kept, focus back to Reply), IME composition ignored.
- Sent line: reply glyph 14 text-3, "You replied" 500 ink, text text-2 one line ellipsis, 8 px under the answers.
- Composer chip: first row in the composer pill, 28 px, radius 12 (18 - 6 inset, concentric), surface-2 like attachment chips, glyph 14 text-3, "Replying to" text-2, quote ink, close x 22 ghost. No blue.
- Chat: message-bubble .quote outlined pill (Messages reply quote pill, which is outlined too) with the same quote string.

## Open / next
- Ask ds to add the Reply button to helpers needHTML so the project-view mockup shows it (asked by message 2026-11-18).

## More CSS notes
- inline reply field: grows open (0fr to 1fr), a gray pill like the composer at 32 px, the send circle pops in with text
- composer quote chip (Reply on a chat message; same quote and cut as the card): first row inside the composer pill, concentric 18 - 6
