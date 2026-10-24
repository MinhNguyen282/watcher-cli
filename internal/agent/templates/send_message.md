Send a message to a background sub-agent started with `agent` and
`run_in_background: true`, and return its current state.

Use this to:

- **Collect a result.** Call with only `to` once the sub-agent has probably
  finished. The tool waits for the in-flight turn, then returns the run's
  state, session, error (if any) and output.
- **Hand back a follow-up.** Pass a `message` to continue the same child
  session. The sub-agent keeps its conversation, so a correction, a narrower
  question or a handback note does not restart the work.

`to` accepts the exact id from the `agent` receipt, the name you gave the
sub-agent, or an unambiguous id prefix.

Do not poll in a loop: one `send_message` with an empty message waits for
the running turn, and the run's completion also publishes a notification.
