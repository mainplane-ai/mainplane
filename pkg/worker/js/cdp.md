# cdp.js

cdp.js, beside this file in `<scratch>/js`, is a Chrome DevTools Protocol (CDP) client for the js interpreter. It has no dependencies. Its source is the full truth of what it does.

```js
const cdp = await import("<scratch>/js/cdp.js")
```

## CDP

- A Chromium browser (Chrome, Edge, Brave, Chromium, and others) with remote debugging on serves one WebSocket per browser process: `ws://<host>:<port>/devtools/browser/<id>`. Nothing else is needed to drive it.
- Every message is JSON. A command is `{id, method, params, sessionId?}`. Chrome answers each one once, with the same id: `{id, result}` or `{id, error}`. An event is `{method, params, sessionId?}` and has no id.
- A target is a page, iframe, worker, or service worker. `Target.getTargets` lists them. `Target.attachToTarget` with `flatten: true` gives a sessionId. Commands and events of that target carry it on the same socket.
- A message with no sessionId is for the browser itself: `Target.*`, `Browser.*`.
- Most domains send events to a session only after `<Domain>.enable` on that session.
- Every method, event, and parameter: https://chromedevtools.github.io/devtools-protocol/

## API

- `cdp.connect(wsUrl, {timeoutMs})` opens the WebSocket and returns a Connection.
- `connection.send(method, params, {timeoutMs, sessionId})` sends one command and returns its result. The client picks the id.
- `connection.attach(targetId)` attaches with `flatten: true` and returns a Target.
- `target.send`, `target.waitFor`, and `target.on` are the Connection's, with the target's sessionId. `target.detach()` detaches it.
- `waitFor(method, {predicate, timeoutMs, since})` returns the params of the first matching event on that connection or target. It sees only events that arrive after the call, unless `since` is given.
- `waitFor` with `since`, a time in ms like `Date.now()`, first searches the log for a match at or after it, then waits. The log's long strings are cut, so a predicate reads them cut.
- `on(method, fn)` calls `fn(params, method)` for each matching event and returns a function that stops it. Method `"*"` matches every event.
- A connection's `on` and `waitFor` see events with no sessionId only. A target's see only its own.
- `connection.close()` closes the socket.
- Every wait defaults to 30 s: connect, send, attach, waitFor.
- An error from Chrome is a `CdpError` with `method`, `code`, `message`, `data`.
- `console.log` of a Connection or Target prints what it points at and the log path.
- `cdp.findBrowsers()` reads `DevToolsActivePort` in each default user data dir of this OS's Chromium browsers and in each dir under `<scratch>/browsers`. It returns `{userDataDir, wsUrl, mtime}` for each. It opens no connection.

## Persistence

- A Connection and a Target are plain objects. On `globalThis` they last until the environment resets. A reset closes the socket.
- The browser is another process. It keeps running and keeps its tabs when the environment resets, unless it was a child of the environment (see Processes).

```js
const cdp = await import("<scratch>/js/cdp.js")
const v = await fetch("http://127.0.0.1:9222/json/version").then(r => r.json())
globalThis.conn = await cdp.connect(v.webSocketDebuggerUrl)
const { targetInfos } = await conn.send("Target.getTargets")
globalThis.tab = await conn.attach(targetInfos.find(t => t.type === "page").targetId)
await tab.send("Page.enable")
const loaded = tab.waitFor("Page.loadEventFired")
await tab.send("Page.navigate", { url: "https://example.com" })
await loaded
```

## Errors without waiting

The client fails these at once, since the thing waited for can never come:

- `waitFor` an event whose switch is off on that connection or target: `<Domain>.enable`, `Target.setDiscoverTargets` for target events, `Browser.setDownloadBehavior` with `eventsEnabled` for download events, `Page.setLifecycleEventsEnabled` for `Page.lifecycleEvent`.
- `send` or `waitFor` on a target after `Target.detachedFromTarget` (tab closed, crashed, or detached). A send or wait that was pending on it fails when that event arrives.
- `send` or `waitFor` on a closed connection. A pending one fails when the socket closes.

## Timeouts

- A send timeout or waitFor timeout error lists the events that arrived on that connection or target meanwhile, and names an open JavaScript dialog.
- A JavaScript alert, confirm, prompt, or beforeunload dialog pauses the page's JavaScript. `Runtime.evaluate` on it answers after `Page.handleJavaScriptDialog`. `Page.javascriptDialogOpening` arrives only with `Page.enable`.
- Chrome may still answer after a send timed out. The answer is in the log with `"late":true`.

## Log

- Every message sent and received on a connection is one line of `<scratch>/js/log/<time>-<pid>-<n>.jsonl`: `{t, dir: "out"|"in", late?, id?, method?, sessionId?, params|result|error}`. `t` is ms since the epoch.
- A string over 2000 characters is cut in the middle: its first and last 1000 remain.
- `rg` finds events in it. It has events of every enabled domain on every attached target, including those no code waited for.

## Where a wsUrl comes from

- A browser launched with `--remote-debugging-port=<port>` and a `--user-data-dir` writes `DevToolsActivePort` there: the port on line 1, the browser's WebSocket path on line 2. `http://127.0.0.1:<port>/json/version` returns `webSocketDebuggerUrl`. Port 0 picks a free port.
- Chrome ignores `--remote-debugging-port` when the user data dir is its default one.
- A browser already running for a user data dir takes a second launch for that dir as a new window, and ignores the second launch's flags.
- A user's own Chrome turns remote debugging on at `chrome://inspect/#remote-debugging`. It then writes `DevToolsActivePort` in its default user data dir and serves no `/json` endpoints. Its socket opens only after the user clicks Allow in Chrome's prompt, so `connect` waits for the click up to its timeout.
- A browser that did not exit cleanly leaves its `DevToolsActivePort`. `mtime` is when it was written.
- One user data dir is one browser process and one endpoint. Its profiles (`Default`, `Profile 1`, ...) share it. `Target.getTargets` lists the targets of every profile, each with its profile's `browserContextId`. `Target.getBrowserContexts` names the default one. `Target.createTarget` with a `browserContextId` works only for the default context and for contexts made with `Target.createBrowserContext`. `Local State` in the user data dir lists profile names and accounts, not context ids.
- A hosted browser provider's API returns a wsUrl.

## Files

- A wsUrl on 127.0.0.1 is a browser on this worker. Its downloads and uploads are files on this worker, which bash, pwsh, and js see.
- A remote wsUrl is a browser on another machine. Its files are there. Bytes cross only over CDP, for example `Network.getResponseBody`, `Fetch.takeResponseBodyAsStream` with `IO.read`, or `Runtime.evaluate`.
- `Browser.setDownloadBehavior` `{behavior: "allow", downloadPath, eventsEnabled: true}` on the connection sets where downloads go. `Browser.downloadWillBegin` and `Browser.downloadProgress` (`state: "completed"`) follow on the connection.
- `DOM.setFileInputFiles` `{files: [path], backendNodeId}` sets a file input from a path on the browser's machine.

## Screenshots

- `Page.captureScreenshot` returns base64 in `data`. Its pixels are device pixels. `Input` events take CSS pixels. `devicePixelRatio` is the ratio.
- A `clip` with `scale: 1 / devicePixelRatio` makes the image CSS pixels, so an image point is a click point.
- The read tool shows a saved image. Calls in one step run in order, so a read after a run in the same step sees what the run wrote.

```js
const fs = await import("node:fs")
const v = (await tab.send("Page.getLayoutMetrics")).cssVisualViewport
const dpr = (await tab.send("Runtime.evaluate", { expression: "devicePixelRatio", returnByValue: true })).result.value
const s = await tab.send("Page.captureScreenshot", { format: "jpeg", quality: 80, clip: { x: v.pageX, y: v.pageY, width: v.clientWidth, height: v.clientHeight, scale: 1 / dpr } })
fs.writeFileSync("<path>.jpg", Buffer.from(s.data, "base64"))
```

## Page JavaScript and input

- The snippet runs in the js process on the worker. Page JavaScript runs only through CDP, for example `Runtime.evaluate` `{expression, returnByValue: true, awaitPromise: true}`.
- `Input.dispatchMouseEvent` (`mouseMoved`, `mousePressed`, `mouseReleased`, with `button` and `clickCount`), `Input.insertText`, and `Input.dispatchKeyEvent` act as a user does.

## Processes

- An environment's end kills every process it started: its process group on Linux and macOS, its process tree on Windows.
- `child_process.spawn` with `detached: true` gives a process its own group on Linux and macOS, so it outlives the environment.
- On Windows, a process started through `cmd /c start "" <exe> ...` has no living parent in the tree, so it outlives the environment. A `detached: true` child does not.
