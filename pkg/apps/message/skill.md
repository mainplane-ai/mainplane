Send a message to another agent by its ID.

Calls go to `$MAINPLANE_URL/apps/message` with `Authorization: Bearer $MAINPLANE_KEY`.

- `POST /apps/message` with JSON `{"to": "<agent id>", "text": "..."}`. The text lands on that agent's live session, via `agent:<your id>`, and wakes it. Returns `{"session", "n"}`.

An agent's ID is its address: there is no list of agents. You know an ID when someone gives it to you, such as an orchestrator naming itself in your task ("reply to agent a1b2c3d4 when done"). The recipient needs no app to receive. Your own ID is in this session's apps note.

Example:

    curl -s -X POST "$MAINPLANE_URL/apps/message" -H "Authorization: Bearer $MAINPLANE_KEY" -d '{"to":"a1b2c3d4","text":"PR 88 is open and CI passed."}'
