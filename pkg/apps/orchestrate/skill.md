# orchestrate: spawn agents of your own, message them, read them, and stop them.

An agent you spawn is a full agent with its own sessions. It may use only workers and apps that you have. You see and control only the agents you spawned.

```
A="Authorization: Bearer $MAINPLANE_KEY"; U=$MAINPLANE_URL/apps/orchestrate
curl -H "$A" -d '{"name":"pr-88","apps":["message"],"message":"<the whole task>"}' $U/spawn   # -> {"id","session"}
curl -H "$A" $U                                   # the agents you spawned
curl -H "$A" $U/<id>                              # one: status, config, apps, sessions, context_used
curl -H "$A" "$U/<id>/records?after=0"            # its live session's records; &session=<sid> for an older one
curl -H "$A" -X PUT -d '{"apps":["message"]}' $U/<id>   # change name, config, apps or notify_on_stop
curl -H "$A" -X POST $U/<id>/stop                 # stop what it is doing
curl -H "$A" -X DELETE $U/<id>                    # stop it and remove the agent; its session files stay
```

- `spawn` takes `name`, `config`, `apps`, `message` and `notify_on_stop`, all optional. Without `config` the agent gets yours. `config` is `{"model","workers":[{"name"}],"context_limit","cold_limit","tools","input","params","preset"}`.
- Workers or apps you do not have are refused with 403. An agent spawned without `orchestrate` cannot spawn.
- The spawned agent knows only what `message` says. It starts on that message; without one it waits. Send it more with the `message` app.
- Nothing tells you when a spawned agent finishes. Either give it the `message` app and your agent id and ask it to report, or pass `"notify_on_stop": true` to get one message `via app:orchestrate` when it next stops.
