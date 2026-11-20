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
- The page runs in a frame with an origin of its own: no key, no cookies, no `localStorage`, and it cannot navigate the interface. Its policy allows inline scripts and styles; scripts, styles, fonts, images and media from any `https:` URL; `fetch` and WebSocket to `https:` and `wss:`; the design system at `/web/ds/1/`. Not allowed: plain `http:`, `eval`, frames, and calls to the harness API (use the calls below). A link opens in a new tab.
- The page reaches its own project, and nothing else, through `mainplane.call(name, args)`, which every dashboard has. It returns a promise:

```
mainplane.call("list", {prefix: "cards/"})                  // -> [{name, size, modified}]
mainplane.call("read", {name: "cards/0001.png"})            // -> ArrayBuffer; make a blob: URL for an image
mainplane.call("write", {name: "state/seen.json", body})    // body: a string, ArrayBuffer or Blob. Also the page's key-value storage
mainplane.call("delete", {name: "state/seen.json"})
mainplane.call("send", {text: "approve card 7"})            // a message to the orchestrator, from the user
mainplane.call("send", {data: {card: 7, action: "approve"}})  // the same, with the value as JSON for the text
mainplane.call("status", {status: "closed", line: "acknowledged"})  // set the status; the orchestrator is not woken
mainplane.call("agents")                                    // -> [{id, name, model, status, updated, session, recent: [{n, kind, time, via, text}]}]: the project's agents and their last records
mainplane.call("open", {agent: id})                         // show that agent's chat; with {agent, session, n} at that record
mainplane.call("open", {file: "cards/0001.png"})            // show a file of the store
mainplane.watch(e => {})                                    // at each change of the store: e.changed [{name, size, modified}], e.deleted [name]
```

- Under the helper it is `parent.postMessage({id, call, ...args}, "*")`, answered by a message `{id, result}` or `{id, error}`; a change of the store arrives as `{event: "changed", changed, deleted}`. When `index.html` itself changes, the page is loaded again.

### Look and components

- The starter `index.html` links `/web/ds/1/mainplane.css` and `/web/ds/1/mainplane.js` and renders three sections from small files: `needs/<id>.json` (questions with answer buttons and Reply), `timeline.json` (lanes of steps) and `cards/<id>.json` (done work: a metric or an image). The file formats are in a comment in it. Keep its layout and fill the files first; change the page only when the project needs more.
- Light and dark mode follow the user's system. Use the tokens (`var(--color-bg)`, `var(--color-text-secondary)`, `var(--space-4)`) and never fixed colors.
- The gallery at `/web/ds/1/` on the harness shows every component. To use one, copy its markup from `/web/ds/1/components/<name>.html`. To change one, copy only the rules you change into your inline style. Do not copy the whole package.
- A Reply on a needs card fires an `mp-reply` event; the starter sends its `detail.text` as a message.
- A button sends a message by default. Use `status` only for a plain acknowledgement that needs no answer.
- Anything that needs a server (a live app, a database, large files) stays on a worker. Link to it from the dashboard.
