# project: use to manage this project's status, status line, and dashboard.

A project is an abstraction used in some of the user connectors. A project bundles one orchestrator agent, agents made under it, a status, and a dashboard. The user sees a list of projects in a sidebar with each one's status and a line, and can open a project to see its dashboard and message its orchestrator. The orchestrator does not work, it orchestrates other agents. It keeps its role and the path of its memory in its instructions.

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
- Every status has a `line`: one short line, at most 100 characters, shown under the project's name, such as "new kimi eval results" or "waiting on the EV cert email".
- When the user or another agent sets the status, the orchestrator gets a system note of it at its next step. The note does not wake an agent.

## Dashboard

- The dashboard is the files of the store. `index.html` is the page; every other file is data the page reads. Names are path segments of letters, digits, dot, dash and underscore. A file is at most 8 MiB and the store 256 MiB. There is no versioning: if you need to keep old results, save them in a worker or remote drive. The dashboard is meant to be a slim current overview of project work.
- Many agents can add one small file per item (`cards/0001.json`, `cards/0001.png`) at once. One large file would require every change to upload again.
- The page runs in a sandbox with no network: no fetch and no outside images. The one exception is the Mainplane design system at `/web/ds/1/` (styles, scripts, fonts only). Your own scripts and styles are inline. The page talks only to the user's interface, by `postMessage`:

```
parent.postMessage({id: 1, call: "list", prefix: "cards/"}, "*")    // -> {id: 1, result: [{name, size, modified}]}
parent.postMessage({id: 2, call: "read", name: "cards/0001.png"}, "*")  // -> {id: 2, result: ArrayBuffer}; make a blob: URL for an image
parent.postMessage({id: 3, call: "send", text: "approve card 7"}, "*")  // a message to the orchestrator, from the user
parent.postMessage({id: 4, call: "status", status: "closed", line: "acknowledged"}, "*")  // set the status; the orchestrator is not woken
// answers and events arrive as message events from parent: {id, result} or {id, error}, and {event: "changed"} when the store changed
```

- The starter `index.html` has a small `call()` helper for this. Start from it.

### Look and components

- The starter `index.html` links `/web/ds/1/mainplane.css` and `/web/ds/1/mainplane.js` and renders three sections from small files: `needs/<id>.json` (questions with answer buttons and Reply), `timeline.json` (lanes of steps) and `cards/<id>.json` (done work: a metric or an image). The file formats are in a comment in it. Keep its layout and fill the files first; change the page only when the project needs more.
- Light and dark mode follow the user's system. Use the tokens (`var(--color-bg)`, `var(--color-text-secondary)`, `var(--space-4)`) and never fixed colors.
- The gallery at `/web/ds/1/` on the harness shows every component. To use one, copy its markup from `/web/ds/1/components/<name>.html`. To change one, copy only the rules you change into your inline style. Do not copy the whole package.
- A Reply on a needs card fires an `mp-reply` event; the starter sends its `detail.text` as a message.
- A button sends a message by default. Use `status` only for a plain acknowledgement that needs no answer.
- Anything that needs a server (a live app, a database, large files) stays on a worker. Link to it from the dashboard.
