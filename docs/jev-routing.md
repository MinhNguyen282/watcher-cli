# Jev routing observer

The MVP runs **observe only**. Before each top-level prompt (including plan
mode and queued prompts when dispatched), one TypeSafe request asks five
independent questions: intent (`Choice`), complexity (`Score`, 0–2), configured
model tier (`Choice`), active skill (`Choice`), and whether the user explicitly
requests confirmation/review (`Noul`). Sub-agent calls and tool iterations do
not create additional observer requests.

Enable it in `crushrc`:

```bash
option jev-observe true
option jev-model jev-latest
```

Supply `TYPESAFE_API_KEY` in the environment of the process running the agent
(the server process in client/server mode). JSON configuration also supports
`options.jev_observe` and `options.jev_model`. The observer is disabled by
default. Enablement sends the current prompt to TypeSafe at
`https://api.typesafe.ai/v1/systemone`; no history, attachments, source files,
provider credentials, or full skill instructions are included. Model IDs and
active skill names/descriptions are included. Protect sensitive prompt content
accordingly. No extra Go dependencies are needed.

The model choices come from configured model slots with enabled providers,
plus the actual current agent model as the authoritative fallback. Skill
choices come from the same active session snapshot supplied to the agent;
skills with `disable-model-invocation` are excluded. Opaque skill IDs avoid
collisions; the selected skill description is displayed beside the ID.
`agent_decides` always remains available.

The TUI displays a status message. `crush run` displays the summary on stderr
without mixing it into the assistant's stdout. The event carries session/run
IDs and also crosses the client/server boundary. Structured Info logs retain
the answers, confidence, probability distributions, returned decision model,
and fallback recommendation, without logging the prompt or API key in this
observer. These logs are diagnostic observations, not a persistent evaluation
dataset or a measured quality result.

If any Choice/Score confidence is below **0.8**, the recommendation falls back
to the current model and normal agent skill selection. Invalid/out-of-list
answers, missing credentials, non-200 responses, or failures use the existing
agent behavior. A request has a three-second deadline, no retry, and a 1 MiB
response limit. Redirects are rejected. Threshold 0.8 is a conservative starting
point, not an empirically calibrated guarantee. An unavailable observer is
reported without failing the agent turn.

Observe never changes the prompt, coding model, enabled skills, loaded skills,
or tool permission checks. The review Noul is advisory and **does not gate tool
calls**. Jev remains separate from the coding model.

## Evaluation before automation

Collect representative prompts with human labels for intent, complexity,
preferred configured tier and skill. Compare observations against those labels,
including ambiguous prompts and cases where no skill fits. Measure agreement,
fallback rate, latency, API failures, and actual coding outcomes/cost. Check
high-confidence errors separately; confidence alone does not establish quality.
Use a stable Jev version for comparable evaluation runs.

Only after evaluation should a separate change introduce explicit automatic
mode and independently calibrated model/skill thresholds. Tool decision gates
belong in a later change at the existing tool execution/permission boundary.
A gate may request confirmation or review; it must never grant permissions or
override a denial. A pre-turn review signal alone cannot assess the risk of a
future concrete tool call.

References: [coding agents](https://docs.typesafe.ai/introduction/coding-agents),
[System One HTTP API](https://docs.typesafe.ai/api/system-one),
[primitives](https://docs.typesafe.ai/primitives),
[confidence](https://docs.typesafe.ai/confidence).
