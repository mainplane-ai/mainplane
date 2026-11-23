# continue: use to hand off to this agent's next session at a point you pick, before the context limit.

```
curl -H "Authorization: Bearer $MAINPLANE_KEY" --data-binary @handoff.md $MAINPLANE_URL/apps/continue/   # -> 202
```

- The body is the handoff text, at most 16 KiB. When the current turn ends (a step with no tool calls), the harness starts the agent's next session at once, as it does at the context limit: a continue record that names this session's log, the agent's instructions, then the handoff text as the first message.
- Messages that this session's steps did not see go to the next session. Asking again before the turn ends replaces the text. A restart of the harness forgets the ask. A call while no turn is running is refused with 409; one from a background job applies to whatever turn is running then.
- A good point is a natural break after a block of work, from about 60-70% of the limit. A handoff that names the goal, the user's latest asks, the state, the next step and the files lets the next session go on without reading much of the log.