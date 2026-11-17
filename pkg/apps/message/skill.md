# message: use to send a message to another agent by its id.

The message app is one simple route. The message arrives in the other agent's session as a user message `via agent:<your id>`, and wakes it.

```
curl -H "Authorization: Bearer $MAINPLANE_KEY" -d '{"to":"<agent id>","text":"PR #88 is open"}' $MAINPLANE_URL/apps/message
```

- An agent's id is its address. There is no list of agents: you can message an agent only when you were given its id, for example by the agent that created you.
- The other agent needs this app to reply. Say who you are and whether to reply.
- 204 means delivered. 404 means no agent has that id.
