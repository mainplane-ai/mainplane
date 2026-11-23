# Needs you card
Questions with answer buttons. The only blue in a dashboard. Reply on any open question puts its title over the chat box under the dashboard (bridge call reply); the user answers there. No inline reply field.

Our own (first project dashboard). Reply: Slack hover action (glyph + word, gray, fill on hover); the answer is written in the chat composer with a quote chip (Messages macOS 26 reply quote).

## CSS notes
Needs you card: questions with answer buttons, the only blue in a dashboard. Needs push-button's .btn and icon-button's .btn.icon.
Each open need has a Reply button at the end of its answer row (gray, plain until hover, like Slack's "Reply" hover action).
Reply calls mainplane.call("reply", {title}): the interface shows "Replying to <title>" over the chat box and puts the cursor there.
The card stays until the orchestrator removes it.

## Choices
- Chosen: Reply is always visible, trailing the answer row, gray glyph + word with no fill until hover (Slack's Reply action). Other option: Show Reply only on hover (Slack hides its actions until hover). (Dashboards are used on phones, where there is no hover, and a question card has few controls, so one quiet button costs little.)
- Chosen: Reply fills the chat composer with a quote chip (bridge call reply). Other option: an inline field in the card, under the answers (built first, removed: no inline replies anywhere).
- Chosen: The quote is the title cut at the last word break before 40 characters, then an ellipsis: Replying to: "Use the EV certificate for the Windows…". Other option: A hard cut at 40 characters mid-word ("...Windows b…"). (Reads as words, and the start of the title is still enough for the orchestrator to match its open question.)
- Chosen: After a reply the card stays; the orchestrator removes or rewrites it. (A reply is not an answer. The orchestrator may come back with a follow-up.)
- Chosen: Composer quote chip (for Reply on a chat message): first row inside the composer pill, gray surface-2 like the attachment chip, reply glyph, "Replying to" in gray, quote in ink, close x. In the chat the quote shows as the reply-quote pill above the sent bubble. Other option: A left gray bar quote (Slack blockquote / WhatsApp). (Same shape family as the attachment chip in the same field, and the sent message matches the Messages reply pill already in the message-bubble item. No blue: blue stays for the needs-you dot and primary answer.)
- Chosen: Reply glyph: the left-turning arrow (Mail and Messages Reply, SF arrowshape.turn.up.left). Other option: Slack's speech bubble glyph (its Reply means a thread). (Apple first; the arrow says reply to this, a bubble says open a thread.)

## Measured
## Files
- needs-card.css, needs-card.js (Reply calls reply), snippet.html (live card, Reply button states, answered card, composer chip, chat).

## Bridge message (dashboard to orchestrator)
- Reply uses the bridge call reply({title}); the parent UI's composer then sends the text below with the user's message.
- text = `Replying to: "<quote>"` + blank line + the user's text, trimmed. Example:
  ```
  Replying to: "Use the EV certificate for the Windows…"

  Wait until Microsoft closes the review, then sign both builds.
  ```
- quote = the need title (h3 text). If it is 40 chars or less, whole and with no ellipsis. Else cut at the last space at or before char 40 (if that leaves at least 24 chars, else a hard cut at 40), strip trailing space and punctuation, add "…" (U+2026). Straight double quotes around it.
- The chat UI shows a user message whose first line matches `^Replying to: "(.+)"$` followed by a blank line as the reply-quote pill (.quote of message-bubble) over the bubble, with the rest as the bubble text. The orchestrator sees plain text.
- Answer buttons are unchanged (they send their own message as before).
- The composer chip builds the text with the same cut, from the card title or the replied-to message's first line.

## Detail list (refs vs ours)
- Reply button: Slack = glyph + word, 13 px bold gray (#5E5D60), no fill until hover, in a floating bar. Ours: .btn.sm 24 px, 13/500, text-2, transparent until hover (fill), text on hover/press, padding 8, trailing in the answer row (margin-left auto). Glyph: arrow (Apple Mail/Messages), not Slack's bubble.
- Composer chip: first row in the composer pill, 28 px, radius 12 (18 - 6 inset, concentric), surface-2 like attachment chips, glyph 14 text-3, "Replying to" text-2, quote ink, close x 22 ghost. No blue.
- Chat: message-bubble .quote outlined pill (Messages reply quote pill, which is outlined too) with the same quote string.

## Open / next

## More CSS notes
- composer quote chip (Reply on a chat message; same quote and cut as the card): first row inside the composer pill, concentric 18 - 6
