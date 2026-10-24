# Plan 07 — DeepSeek Thinking Effort Commands

> Priority: **#3** · Difficulty: ⭐ · Value: High for DeepSeek users ·
> Harness2 fit: Direct

## 1. Goals

Expose DeepSeek's thinking-mode effort controls in the CLI and TUI:

- `low`, `high`, and `max` effort levels, with `high` as the default.
- `none` to disable thinking mode.
- Command-palette shortcuts under `/efforts`.
- The existing `--reasoning-effort` flag accepts the same values.

## 2. DeepSeek API facts

From DeepSeek's thinking-mode guide:

- Thinking is enabled by default with effort `high`.
- OpenAI-format requests use `reasoning_effort` (`low`, `high`, `max`)
  plus `thinking: {"type": "enabled" | "disabled"}`.
- `none` is a disabling choice, not a numeric effort level.
- OpenAI-compatible clients must send `thinking` in `extra_body`.

## 3. Design

### 3.1 Catalog metadata

`normalizeDeepSeekCatalog` marks reasoning models with:

```go
ReasoningLevels:        []string{"low", "high", "max", "none"}
DefaultReasoningEffort: "high"
```

Existing catalog metadata wins. Non-reasoning DeepSeek models are left
unchanged. This keeps the UI and CLI driven by model metadata instead of
hardcoded effort lists.

### 3.2 Provider options

For the DeepSeek provider:

- A selected effort in `low|high|max` sends `reasoning_effort` and
  enables `thinking`.
- `none` omits `reasoning_effort` and sends
  `thinking: {"type": "disabled"}`.
- No selected effort keeps the previous `Think` behavior.

### 3.3 Commands

The command palette gains:

- `Select Reasoning Effort` with aliases `efforts`, `effort`, and
  `thinking effort`.
- DeepSeek-only direct entries: `Set Effort: Low`, `Set Effort: High`,
  `Set Effort: Max`, and `Disable Thinking`, each with `/efforts ...`
  aliases.

Typing `/efforts` opens the selector; typing `/efforts max` filters to the
direct command.

### 3.4 Adjacent best practices adopted from DeepSeek Harness

- **Explicit capability seams:** `internal/verify` splits detection,
  target selection, execution, and parsing instead of hiding them in the
  tool body.
- **Design tool UI presentation up front:** `verify` has a dedicated chat
  renderer rather than the generic fallback.
- **Fail loud on misconfiguration:** malformed project manifests surface
  the parse error instead of silently becoming "no project found".
- **Bounded model-visible output:** verification failures are typed,
  deduplicated, and capped; raw output keeps only a bounded tail.

## 4. Acceptance Criteria

- [x] DeepSeek Flash and Pro expose `low`, `high`, `max`, and `none`.
- [x] Explicit catalog efforts are preserved.
- [x] `none` disables thinking and omits `reasoning_effort`.
- [x] `/efforts` and `/efforts <level>` entries appear for DeepSeek.
- [x] `watcher run --reasoning-effort none` validates for DeepSeek.
- [x] Malformed manifests fail with their parse error.
- [x] Focused config, agent, dialog, chat, verify, and repo-map tests pass.

## 5. Files

| File | Change |
| --- | --- |
| `internal/config/deepseek.go` | Effort metadata and defaults |
| `internal/agent/coordinator.go` | DeepSeek `none` handling |
| `internal/ui/dialog/commands.go` | `/efforts` entries |
| `internal/ui/model/ui.go` | Close the palette after direct effort selection |
| `internal/ui/chat/verify.go` | Verify tool renderer |
| `internal/verify/verify.go` | Fail-loud manifest errors |
| `README.md` | User-facing effort usage |
