# message: send a message to another agent by its id.

One route. The message arrives in the other agent's session as a user message `via agent:<your id>`, and wakes it.

```
curl -H "Authorization: Bearer $MAINPLANE_KEY" -d '{"to":"<agent id>","text":"PR #88 is open"}' $MAINPLANE_URL/apps/message
```

- An agent's id is its address. There is no list of agents: you can message an agent only when you were given its id, for example by the agent that spawned you.
- The other agent needs this app only to answer. Say who you are and how to reply when you expect an answer.
- 204 means delivered. 404 means no agent has that id.
