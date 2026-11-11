Spawn, watch, update, stop and delete subagents of this agent.

Calls go to `$MAINPLANE_URL/apps/orchestrate` with `Authorization: Bearer $MAINPLANE_KEY`, JSON in and out. You reach only the agents you spawned.

- `POST /apps/orchestrate/spawn` with `{"name", "config", "apps", "task", "notify_on_stop"}`, all optional. `config` is an agent config (`model`, `tools`, `workers`, `context_limit`, `cold_limit`, `input`, `params`, `preset`); omitted, it is yours. `apps` defaults to none. `task` is the first message, which starts the agent; without it the agent waits. Returns `{"id", "session"}`.
- `GET /apps/orchestrate/agents`: the agents you spawned, newest updated first, each with `status` (`stepping`, `open`, `closed`, `failed`), `current_session`, `sessions` and `context_used`.
- `GET /apps/orchestrate/agents/<id>`: one of them.
- `GET /apps/orchestrate/agents/<id>/records?session=<sid>&after=<n>&wait=30s`: the records of one of its sessions (default its live one) after record n, in the state file format: a JSON header line, then `len` bytes of body, then a newline. `wait` long polls for new records.
- `PUT /apps/orchestrate/agents/<id>` with any of `{"name", "config", "apps"}`. A new model or tools takes effect at its next message, in a new session.
- `POST /apps/orchestrate/agents/<id>/watch` with `{"notify_on_stop": true}` or `false`.
- `POST /apps/orchestrate/agents/<id>/stop`: stop what it is doing.
- `DELETE /apps/orchestrate/agents/<id>`: stop it and remove it. Its sessions' records stay.

A subagent's workers must be among your workers and its apps among your apps (403 otherwise), so it can never do more than you. Without `orchestrate` in its apps it cannot spawn agents itself.

By default nothing tells you when a subagent finishes. Either ask it in its task to report to you with the message app ("send agent <your id> a message when done", if it has `message`), or pass `"notify_on_stop": true`: then one message from `app:orchestrate` reaches you the first time it stops. Rolling over to a new session when its context fills is not a stop. To send a subagent more work, use the message app with its ID.

Example:

    curl -s -X POST "$MAINPLANE_URL/apps/orchestrate/spawn" -H "Authorization: Bearer $MAINPLANE_KEY" -d '{"name":"tests","apps":["message"],"task":"Run the test suite in ~/repo and send agent a1b2c3d4 a message with the result.","notify_on_stop":true}'
