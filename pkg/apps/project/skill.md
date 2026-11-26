# project: use to manage this project's status, status line, and dashboard.

A project is an abstraction used in some of the user connectors. 
A project bundles one orchestrator agent, agents made under it, a status, and a dashboard. 
The user sees a list of projects in a sidebar with each one's status and a line.
The project exists to take cognitive load off the user. 
The user mainly looks only at the dashboard and sends messages mainly to the orchestrator, which orchestrates the other agents and maintains the status and the dashboard.
The sidebar lists open projects first (the UI says "needs you" to the user), then waiting, then closed. A project that is open and actively working is sorted below one that is stopped, which is more likely blocked on the user.
A click on a project shows its dashboard first. The orchestrator's chat, and the chat of any agent, open only when the user clicks to view them. A chat shows user messages and the agent's text output; calls and messages from apps, agents and jobs are collapsed.
It is recommended that every agent of the project reads `/conversation` when it starts and before it asks the user anything: the user's own words, without relaying them.

```
A="Authorization: Bearer $MAINPLANE_KEY"; U=$MAINPLANE_URL/apps/project
curl -H "$A" $U/                                                    # the project: id, name, orchestrator, status, line
curl -H "$A" -X PUT -d '{"status":"waiting","line":"waiting on the EV cert"}' $U/status
curl -H "$A" $U/models                                              # the models connected now, each with a full config for orchestrate
curl -H "$A" $U/conversation                                        # the user's messages and the orchestrator's answers to them, the newest 10k tokens as text
curl -H "$A" "$U/files/?prefix=cards/"                              # list: [{"name","size","modified"}]
curl -H "$A" $U/files/index.html                                    # read one file
curl -H "$A" -X PUT --data-binary @index.html $U/files/index.html   # write one file, whole
curl -H "$A" -X DELETE $U/files/cards/0001.json
```

## Status

- `open`: the user can act on something now. `waiting`: the project waits on something later or outside (an email, a scan, a date), and there is nothing for the user to do now; watch for it with `schedule`, and let the line name what it waits on. `closed`: nothing needs the user.
- Every status has a `line`, max 100 characters, e.g. "new kimi eval results" or "waiting on the EV cert email".
- The orchestrator maintains the status at all times.

## Dashboard

- The dashboard is the files of the store. `index.html` is the page; every other file is data the page reads. Names are path segments of letters, digits, dot, dash and underscore. A file is at most 8 MiB and the store 256 MiB. There is no versioning: if you need to keep old results, save them in a worker or remote drive. The dashboard is meant to be a slim current overview of project work.
- Many agents can add one small file per item (`cards/0001.json`, `cards/0001.png`) at once. One large file would require every change to upload again. Edit the live files; when you push from a local copy, also delete what is gone.
- The page runs in a frame with an origin of its own: no key, no cookies, no `localStorage`, and it cannot navigate the interface. Its policy allows inline scripts and styles, and `eval`; scripts, styles, fonts, images and media from any `https:` URL; `fetch` and WebSocket to `https:` and `wss:`; frames of `https:`, `blob:` and `data:` URLs and of `srcdoc`; workers from `blob:` URLs; the design system at `/web/ds/1/`. Not allowed: plain `http:`. The page has no key for the harness API. The calls below are for harness function. A link opens in a new tab. A picture at least 80 px wide clicked (not in a link or a button) opens large over the interface, in place of the page's own handling of that click; one from another site without CORS opens in a new tab. Do not build a zoom of your own.
- The page reaches its own project, and nothing else, through `mainplane.call(name, args)`, which every dashboard has. It returns a promise:

```
mainplane.call("list", {prefix: "cards/"})                  // -> [{name, size, modified}]
mainplane.call("read", {name: "cards/0001.png"})            // -> ArrayBuffer
mainplane.preview("cuts/0001.mp4")                          // -> a blob: URL of the file, typed by its name: the src of an <img>, <video>, <audio>, or of an <iframe> for HTML
mainplane.call("write", {name: "state/seen.json", body})    // body: a string, ArrayBuffer or Blob. Also the page's key-value storage
mainplane.call("delete", {name: "state/seen.json"})
mainplane.call("send", {text: "approve card 7"})            // a message to the orchestrator, from the user
mainplane.call("send", {data: {card: 7, action: "approve"}})  // the same, with the value as JSON for the text
mainplane.call("status", {status: "closed", line: "acknowledged"})  // set the status; the orchestrator is not woken
mainplane.call("reply", {title: "Approve the new logo?"})  // put "Replying to <title>" over the chat box under the dashboard and the cursor in it; the user's message goes with the title
mainplane.call("agents")                                    // -> [{id, name, model, status, updated, session, recent: [{n, kind, time, via, text}]}]: the project's agents and their last records
mainplane.call("open", {agent: id})                         // show that agent's chat; with {agent, session, n} at that record
mainplane.call("open", {file: "cards/0001.png"})            // show a file of the store
mainplane.watch(e => {})                                    // at each change of the store: e.changed [{name, size, modified}], e.deleted [name]
```

- Under the helper it is `parent.postMessage({id, call, ...args}, "*")`, answered by a message `{id, result}` or `{id, error}`; a change of the store arrives as `{event: "changed", changed, deleted}`. When `index.html` itself changes, the page is loaded again.
- Show a rich file of the store (an article, a report, a mockup) in a nested frame: `frame.src = await mainplane.preview("articles/draft.html")`, or `frame.srcdoc` with its text when it links the design system (only there does `/web/ds/1/` resolve). The nested page has no `mainplane` and cannot read the store or the dashboard's `blob:` URLs, so make it one self-contained file. A PDF does not show in a frame.

### Look and components

- The starter `index.html` links `/web/ds/1/mainplane.css` and `/web/ds/1/mainplane.js` and renders three sections from small files, with the formats in a comment in it:
  - `needs/<id>.json`: what the user can act on now, each with answer buttons and Reply. Every item here takes the user's attention, so it is worth keeping exact: remove an item once it is settled, also when it was settled only in chat.
  - `timeline.json`: lanes of steps, to re-orient the user at a glance. A `wait` step names what it waits on in its label or value, which answers the user's usual question about a waiting project.
  - `cards/<id>.json`: finished work as a metric or an image, a visual-first view of the progress.
- Keep the starter's layout and fill the files first; the layout can change completely when the user asks or the project needs more.
- Light and dark mode follow the user's system. Use the tokens (`var(--color-bg)`, `var(--color-text-2)`, `var(--space-4)`) instead of fixed colors.
- The gallery at `/web/ds/1/` on the harness shows every provided component. To use one, copy its markup from `/web/ds/1/components/<name>.html`. To change one, copy only the rules you change into your inline style. Do not copy the whole package.
- A Reply on a needs card calls `reply`: the user answers in the chat box under the dashboard. There are no inline reply fields.
- Use `status` for a plain acknowledgement that needs no answer.