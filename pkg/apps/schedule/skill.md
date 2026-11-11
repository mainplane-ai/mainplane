One-shot and cron timers that post a message to this agent when they fire.

A timer posts a message to this agent, via `app:schedule`, when it fires. It names the agent, not the session, so it reaches whichever session is live then, and it survives harness restarts. Nothing is posted for you otherwise.

Calls go to `$MAINPLANE_URL/apps/schedule` with `Authorization: Bearer $MAINPLANE_KEY`.

- `POST /apps/schedule` with JSON `{"text": "...", "in": "90m"}`, `{"text": "...", "at": "2026-11-12T09:00:00Z"}` or `{"text": "...", "cron": "0 9 * * 1-5"}`. `in` is a duration like `45s`, `90m` or `2h30m`. `cron` is five fields (minute hour day-of-month month day-of-week) in UTC, with `*`, lists, ranges and `/` steps. Returns the timer: `{"id", "agent", "text", "next"}`, and `"cron"` for a cron timer.
- `GET /apps/schedule`: this agent's timers.
- `DELETE /apps/schedule/<id>`: remove a timer (a one-shot timer removes itself when it fires).

The message reads `Timer <id> fired: <text>`. Write the text as the instruction you want to receive then: the session that reads it may not remember setting it.

Example:

    curl -s -X POST "$MAINPLANE_URL/apps/schedule" -H "Authorization: Bearer $MAINPLANE_KEY" -d '{"in":"30m","text":"Check whether CI passed on PR 88."}'
