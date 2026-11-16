# project: this agent's project: its status and line in the user's sidebar, and the files of its dashboard.

A project is one orchestrator agent, the agents made under it, a status, and a dashboard. The user sees a list of projects with each one's status and line, and opens a project to see its dashboard and to message its orchestrator. Every agent of the project that has this app may call it.

```
A="Authorization: Bearer $MAINPLANE_KEY"; U=$MAINPLANE_URL/apps/project
curl -H "$A" $U/                                                    # the project: id, name, orchestrator, status, line
curl -H "$A" -X PUT -d '{"status":"waiting","line":"waiting on the EV cert"}' $U/status
curl -H "$A" $U/models                                              # the models connected now, each with a full config for orchestrate
curl -H "$A" "$U/files/?prefix=cards/"                              # list: [{"name","size","modified"}]
curl -H "$A" $U/files/index.html                                    # read one file
curl -H "$A" -X PUT --data-binary @index.html $U/files/index.html   # write one file, whole
curl -H "$A" -X DELETE $U/files/cards/0001.json
```

## Status

- `open`: the user should look. `waiting`: the project waits on something outside, and there is nothing for the user to do. `closed`: nothing needs the user. Closed stops nothing: set it open again when there is something to see.
- Every status has a `line`: one short line, at most 100 characters, shown under the project's name, such as "new kimi results" or "waiting on the EV cert".
- When the user or another agent sets the status, the orchestrator reads a system note of it at its next step. The note wakes nobody.

## Dashboard

- The dashboard is the files of the store. `index.html` is the page; every other file is data the page reads. Names are path segments of letters, digits, dot, dash and underscore. A file is at most 8 MiB and the store 256 MiB. There are no old versions: keep sources and history in the project's folder on a worker and upload the result.
- Write one small file per item (`cards/0001.json`, `cards/0001.png`), not one large file that every change uploads again. Many agents can then add items at once.
- The page runs in a sandbox with no network: no fetch, no outside scripts, styles, fonts or images. Scripts and styles are inline. It talks only to the user's interface, by `postMessage`:

```
parent.postMessage({id: 1, call: "list", prefix: "cards/"}, "*")    // -> {id: 1, result: [{name, size, modified}]}
parent.postMessage({id: 2, call: "read", name: "cards/0001.png"}, "*")  // -> {id: 2, result: ArrayBuffer}; make a blob: URL for an image
parent.postMessage({id: 3, call: "send", text: "approve card 7"}, "*")  // a message to the orchestrator, from the user
parent.postMessage({id: 4, call: "status", status: "closed", line: "acknowledged"}, "*")  // set the status; the orchestrator is not woken
// answers and events arrive as message events from parent: {id, result} or {id, error}, and {event: "changed"} when the store changed
```

- The starter `index.html` has a small `call()` helper for this. Start from it.
- A button sends a message by default. Use `status` only for a plain acknowledgement that needs no answer.
- Anything that needs a server (a live app, a database, large files) stays on a worker. Link to it from the dashboard.
