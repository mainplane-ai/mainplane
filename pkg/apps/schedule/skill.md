Schedule one-shot timers and recurring cron messages to wake this agent later.

Routes (via `mainplane app schedule <METHOD> <path> [body]` or `$MAINPLANE_URL/apps/schedule`):

- `GET /` -> list this agent's scheduled jobs (`[{"id","text","cron","at","next_run","created"}]`).
- `POST /` -> create a timer or cron job. Body JSON requires `"text"` and one of:
  - `"in"`: Go duration from now, e.g. `{"text": "Check build", "in": "15m"}`
  - `"at"`: RFC3339 timestamp, e.g. `{"text": "Daily check", "at": "2026-11-12T09:00:00Z"}`
  - `"cron"`: 5-field UTC cron (`min hour dom mon dow`), e.g. `{"text": "Sync", "cron": "*/30 * * * *"}`
- `DELETE /{id}` -> cancel scheduled job `{id}` (`204`).

Timers persist across harness restarts and session rollovers and post with `via: "app:schedule"`.
