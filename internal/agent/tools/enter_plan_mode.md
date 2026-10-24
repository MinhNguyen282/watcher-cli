# Enter Plan Mode

Switch the session into plan mode, a read-only phase for investigating and
designing before changing anything.

Use this when the user asks for a plan, or when a task is large enough that
the approach should be agreed before code changes. While plan mode is
active, mutating tools (edit, write, bash commands that change state, and
the mutating LSP tools) are refused by the harness, and shell access is
limited to read-only commands.

Write the plan into your response and call `exit_plan_mode` to hand it back
for approval. Plan mode ends only through that call, so do not ask the user
to "switch back" themselves.

Arguments: none.
