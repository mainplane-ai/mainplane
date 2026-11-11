# schedule: timers that message this agent later, once or on a cron.

A timer posts `[timer <id>] <text>` to you as a user message `via app:schedule` when it is due. It is addressed to the agent, not the session: it survives harness restarts and reaches whichever session is live then. Write the text for a reader who has lost this session's context.

```
A="Authorization: Bearer $MAINPLANE_KEY"
curl -H "$A" -d '{"in":"45m","text":"check the deploy"}' $MAINPLANE_URL/apps/schedule
curl -H "$A" -d '{"at":"2026-12-01T09:00:00Z","text":"send the report"}' $MAINPLANE_URL/apps/schedule
curl -H "$A" -d '{"cron":"0 9 * * 1-5","text":"morning triage"}' $MAINPLANE_URL/apps/schedule
curl -H "$A" $MAINPLANE_URL/apps/schedule                # your timers
curl -H "$A" -X DELETE $MAINPLANE_URL/apps/schedule/<id>
```

- Give exactly one of `in` (a duration: 90s, 45m, 36h), `at` (RFC 3339) or `cron`. The answer is the timer with its `id` and `next` time.
- `cron` is five fields in UTC: minute hour day-of-month month day-of-week (0 is Sunday). Each is `*`, a number, `a-b`, a list `a,b`, or any of these with a step `/n`. All five must match.
- A one-shot timer is gone once it fires. A cron timer fires until you delete it.
