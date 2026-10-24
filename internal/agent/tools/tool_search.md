# Tool Search

Load the schema of a tool that is not currently in your tool list, instead
of guessing its name or arguments.

Some tools, usually MCP server tools, are deferred: their schemas are left
out of the window until you need them. Search with plain words describing
what you want to do ("create a github issue", "query postgres"), then call
the loaded tool directly. Loaded tools stay available for the rest of the
session.

Arguments: `query` — a short capability description, not a tool name.
