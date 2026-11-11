Spawn, inspect, update, watch, stop, and delete subagents within your worker and app permissions.

Routes (via `mainplane app orchestrate <METHOD> <path> [body]` or `$MAINPLANE_URL/apps/orchestrate`):

- `POST /spawn` -> `{"name"?, "config"?, "apps"?, "text"?, "notify_on_stop"?}` -> `{"id", "session"}`.
  `config` defaults omitted fields (`model`, `context_limit`, `workers`, `tools`) from your agent. `target.workers` and `target.apps` must be subsets of yours (`403` otherwise). Spawned agents get `via: "agent:<your_id>"`.
- `GET /list` (or `GET /`) -> list agents (`?via=`, `?status=`, `?limit=`).
- `GET /get/{id}` -> get `AgentInfo` for agent `{id}`.
- `GET /records/{sid}` -> read records of session `{sid}` (`?after=N`, `?wait=10s`, `?format=json`).
- `POST /update/{id}` -> `{"name"?, "config"?, "apps"?, "notify_on_stop"?}` (subset rule enforced).
- `POST /watch/{id}` -> subscribe once to a `via: "app:orchestrate"` notice when `{id}` stops.
- `POST /stop/{id}` -> stop agent `{id}` (`204`).
- `DELETE /{id}` (or `POST /delete/{id}`) -> delete agent `{id}` (`204`).
