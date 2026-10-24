# Exit Plan Mode

Present the plan you built while in plan mode and request approval to leave
it. Leave plan mode only through this tool: the harness refuses mutating
tools until the user approves.

Provide the complete plan as `plan`. The user sees it in the approval
prompt, so make it self-contained: what will change, in what order, and how
it will be verified. If the user rejects it, revise the plan and call this
tool again.
