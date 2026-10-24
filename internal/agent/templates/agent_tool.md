Launch a new agent to research or plan, and report back a single result.

Subagent types:

- `task` (default): a fresh agent with read-only search tools and no
  conversation history. Use it when finding the answer would take several
  rounds of searching whose output you do not need afterwards.
- `explore`: the same agent with its tool list filtered to read-only tools.
  It cannot edit files or run shell commands that change state.
- `plan`: the configured plan agent. Use it when the user asks for an
  implementation plan rather than an answer.
- `fork`: an agent that inherits your conversation context and then works
  independently. Use it for follow-up work that depends on everything
  discussed so far; it sees the recent turns, not only your prompt.

Do not duplicate work you delegated. If you delegate research, wait for the
report instead of running the same searches yourself. Give the subagent a
self-contained prompt: it does not see your reasoning, only what you write.
