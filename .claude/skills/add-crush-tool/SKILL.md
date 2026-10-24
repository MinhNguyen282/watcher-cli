---
name: add-crush-tool
description: Add a new model-callable tool under internal/agent/tools. Use when the user asks for a new agent tool, and verify every registration point before declaring it done.
allowed-tools: Read, Grep, Glob, Edit, Write, Bash
---

# Add an agent tool

`AGENTS.md` is the source of truth. A tool is visible only when it is
registered everywhere below. A missing registration is a silent behavioral
bug, not a compile error.

## 1. Create the tool

Add `internal/agent/tools/<name>.go` following a recent tool of the same
shape. For a read-only tool, start from `ls.go` or `view.go`; for a mutating
tool, start from `write.go` or `edit.go`.

Requirements:

- A `New<Name>Tool(...) fantasy.AgentTool` constructor. Pass
  `permission.Service` when the tool can mutate files, processes, or the
  network.
- A params struct with `json` tags and `description` tags. Those descriptions
  are the model-facing schema.
- An embedded `<name>.md` description with a `//go:embed` directive. Use
  `<name>.md.tpl` rendered with `renderTemplate` when the description needs
  runtime data.
- Tool bodies call `tools.RequestApproval`, never the permission service
  directly.
- Return `fantasy.ToolResponse`; do not leak raw Go errors.
- Bound the output with a visible truncation marker when the result can be
  large.

## 2. Register it in the coordinator

Add the constructor to `buildTools` in `internal/agent/coordinator.go`,
alongside the other `allTools = append(...)` entries. Respect the existing
`isSubAgent` and `interactive` gates for tools that must not be available to
sub-agents.

## 3. Add it to the config allowlist

Add the name to `allToolNames()` in `internal/config/config.go`. If the
default `task` or `plan` agents should have the tool, also update
`resolveReadOnlyTools` and `resolvePlanTools` in the same file.

## 4. Add its harness spec

Add a `Spec` to `toolSpecs` in `internal/agent/tools/spec.go`:

- `Name`
- `RuleContentField` (for example `file_path` or `command`; empty when the
  tool has no content-scoped permission rules)
- `ReadOnly` or `Mutating`
- `SearchHint`
- `Deferrable`
- `MaxResultChars`
- `Backgrounding`

A tool missing from `toolSpecs` is treated as mutating, with no content rule
and no backgrounding. See `AGENTS.md` gotcha 7.

## 5. Add a UI renderer

Add a case to `NewToolMessageItem` in `internal/ui/chat/tools.go`, or accept
the generic renderer intentionally. Do not leave it to chance.

## 6. Tests

Add tests in `internal/agent/tools/<name>_test.go`:

- The happy path.
- A negative control for the permission or guard path when the tool touches
  files, processes, or the network. A happy-path test alone is not evidence
  for a guard (`docs/testing.md`).

## 7. Verify

```sh
go build ./...
go test ./internal/agent/tools/... ./internal/config/... ./internal/agent/...
```

Then grep to prove every registration point is present:

```sh
grep -n '"<name>"' internal/config/config.go
grep -n '<Name>ToolName' internal/agent/tools/spec.go
grep -n 'New<Name>Tool' internal/agent/coordinator.go internal/agent/tools/<name>.go
```

All three must be non-empty. If the tool is deferrable, confirm tool search
can find it.
