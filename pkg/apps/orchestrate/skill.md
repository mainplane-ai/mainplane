# orchestrate: create agents of your own, read them, change them, and stop them.

This is the harness's /agents API for the agents you created. An agent you create is a full agent with its own sessions. It may use only workers and apps that you have. You see and control only the agents you created.

```
A="Authorization: Bearer $MAINPLANE_KEY"; U=$MAINPLANE_URL/apps/orchestrate/agents
curl -H "$A" -d '{"name":"pr-88","apps":["message"],"message":"<the whole task>"}' $U   # -> {"id","session"}
curl -H "$A" $U                                   # the agents you created; ?status= ?limit= ?after=<id>
curl -H "$A" $U/<id>                              # one: status, config, apps, sessions, context_used, usage
curl -H "$A" "$U/<id>/records?after=0"            # its live session's records; &session=<sid> for an older one, &wait=30s to wait for new ones
curl -H "$A" -X PUT -d 'pr-88 review' $U/<id>/name              # the name as text
curl -H "$A" -X PUT -d '{"model":"...","workers":[{"name":"..."}]}' $U/<id>/config   # the whole config
curl -H "$A" -X PUT -d '["message"]' $U/<id>/apps               # the whole list of apps
curl -H "$A" -X PUT -d 'true' $U/<id>/notify_on_stop            # true or false
curl -H "$A" -X POST $U/<id>/stop                 # stop what it is doing
curl -H "$A" -X DELETE $U/<id>                    # stop it and remove the agent; its session files stay
```

- A create takes `name`, `config`, `apps`, `message` and `notify_on_stop`, all optional. Without `config` the agent gets yours. `config` is `{"model","workers":[{"name"}],"context_limit","cold_limit","tools","input","params","preset"}`.
- Workers or apps you do not have are refused with 403. An agent that is not yours is 404. An agent created without `orchestrate` cannot create agents.
- The agent you create knows only what `message` says. It starts on that message; without one it waits. Send it more with the `message` app.
- Nothing tells you when an agent you created finishes. Either give it the `message` app and your agent id and ask it to report, or pass `"notify_on_stop": true` to get one message `via app:orchestrate` when it next stops.
