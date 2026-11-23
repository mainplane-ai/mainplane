# orchestrate: use to create agents, read them, change them, and stop them.

This is the harness's /agents API for the agents you created. An agent you create is a full agent with its own sessions. It may use only workers and apps that you have. You see and control only the agents you created.

```
A="Authorization: Bearer $MAINPLANE_KEY"; U=$MAINPLANE_URL/apps/orchestrate/agents
curl -H "$A" -d '{"name":"Review PR 88","apps":["message"],"message":"<the whole task>"}' $U   # -> {"id","session"}
curl -H "$A" $U                                   # the agents you created; ?status= ?limit= ?after=<id>
curl -H "$A" $U/<id>                              # one: status, config, apps, sessions, context_used, usage
curl -H "$A" "$U/<id>/records?after=0"            # its live session's records; &session=<sid> for an older one, &wait=30s to wait for new ones
curl -H "$A" -X PUT -d 'Review PR 88 again' $U/<id>/name              # the name as text
curl -H "$A" -X PUT -d '{"model":"...","workers":[{"name":"..."}]}' $U/<id>/config   # the whole config
curl -H "$A" -X PUT -d '["message"]' $U/<id>/apps               # the whole list of apps
curl -H "$A" -X PUT --data-binary @file $U/<id>/instructions    # the instructions as text; empty removes them
curl -H "$A" -X PUT -d 'true' $U/<id>/notify_on_stop            # true or false
curl -H "$A" -X POST $U/<id>/stop                 # stop what it is doing
curl -H "$A" -X DELETE $U/<id>                    # stop it and remove the agent; its session files stay
```

- A create takes `name`, `config`, `apps`, `instructions`, `message` and `notify_on_stop`, all optional. Without `config` the agent gets yours. A `config` you give needs only `model`, and `workers` when it should differ from yours. Every field you leave out comes from the model's preset in the catalog: `context_limit`, `tools`, `input`, `params`. Leave `context_limit` unset to use the preset's recommended limit (250000 for most models). The field shape is `{"model","workers":[{"name"}],"context_limit","cold_limit","tools","input","params","preset"}`.
- The user sees an agent's name in agent lists and over its chat. A few plain words for its job read better there than an id or a code.
- `instructions` is text that every session of the agent starts with, at most 4096 bytes. It is recommended to point to the paths it must read, and have long lived agents keep memory files. In the instructions route `<id>` may also be your own agent id. No other route takes your own id.
- An agent you create gets the recommended `cold_limit` 100000 unless its `config`, or yours when it has none, sets another.
- Workers or apps you do not have are refused with 403. An agent that is not yours is 404. An agent created without `orchestrate` cannot create agents.
- It starts on its first message; without one it waits. Send it more with the `message` app.
- Nothing tells you when an agent you created finishes. Either give it the `message` app and your agent id and ask it to report, or pass `"notify_on_stop": true` to get one message `via app:orchestrate` when it next stops.

## Orchestrating

- You do not do project work yourself. Agents you create do it, so you stay responsive to answer the user. Access workers only to oversee or intervene if really needed.
- Use short timeouts on your own calls, so you do not hang for the full default of 10 minutes.
- Take ownership of the agents you orchestrate. The user cares that the work gets done, not who did it; mention agents only when asked.
- Your text output is what the user reads in your chat, and calls and messages from apps, agents and jobs show collapsed. Keep the text to what the user should see, without chatter or agents' status reports.
- It is recommended not to give the agents you create `orchestrate`: one orchestrator keeps the work and the user's view in one place.
