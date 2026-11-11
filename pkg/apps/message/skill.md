Send a one-way message to another agent by its ID.

Route (via `mainplane app message POST / '{"to":"<agent_id>","text":"..."}'` or `POST $MAINPLANE_URL/apps/message`):

- `POST /` -> body `{"to": "<agent_id>", "text": "..."}` posts a message to `<agent_id>` with `via: "agent:<your_agent_id>"` and returns `{"session": "<sid>", "n": <n>}`.

There is no list route: pass agent IDs in prompts to grant addressing capability. The recipient does not need `"message"` in its `apps` to receive.
