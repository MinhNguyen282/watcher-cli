# Implementation Plans — watcher-cli

Các file plan dưới đây được viết sau khi khảo sát codebase trên branch
`main` (commit sau pull mới nhất). Mỗi file là một gói công việc **độc lập**,
tự chứa đủ bối cảnh kiến trúc (file/type chính xác) để một agent khác có thể
implement mà không cần khảo sát lại từ đầu.

## Danh sách & thứ tự khuyến nghị

| # | Plan | Độ khó | Phụ thuộc | Ghi chú delegate |
|---|---|---|---|---|
| 1 | [01-shadow-git-checkpoints.md](01-shadow-git-checkpoints.md) | ⭐⭐ | — | Làm trước: 5C (revert hunk) và harness2 đều hưởng lợi. |
| 2 | [02-multi-model-routing.md](02-multi-model-routing.md) | ⭐⭐ | — | Độc lập hoàn toàn, giá trị chi phí cao, nên làm sớm. |
| 3 | [03-watchdog-mode.md](03-watchdog-mode.md) | ⭐⭐⭐ | 02 (tùy chọn) | Có thể giao song song với 01/02. |
| 4 | [04-treesitter-repo-map.md](04-treesitter-repo-map.md) | ⭐⭐⭐⭐ | — | Chú ý ràng buộc CGO: tầng A (pure Go) ship trước. |
| 5 | [05-sdlc-review-db-inspector.md](05-sdlc-review-db-inspector.md) | ⭐⭐⭐→⭐⭐⭐⭐ | 01 (cho phần 5C) | Chia 3 phần 5A/5B/5C, delegate riêng từng phần. |
| 6 | [06-verify-live-repomap.md](06-verify-live-repomap.md) | ⭐⭐⭐ | — | Vòng verify tất định + repo map incremental, ưu tiên cho coding task. |
| 7 | [07-deepseek-effort-commands.md](07-deepseek-effort-commands.md) | ⭐ | — | `/efforts` cho DeepSeek (low/high/max/none) + best practices từ deepseek-harness. |
| 8 | [08-deepseek-harness-best-practices.md](08-deepseek-harness-best-practices.md) | ⭐⭐⭐⭐ | — | Kế hoạch đầy đủ áp dụng tool pipeline, job/session, compaction, UI, testing từ deepseek-harness. |
| 9 | [09-claude-code-harness-port.md](09-claude-code-harness-port.md) | ⭐⭐⭐⭐⭐ | 08 (P0 pipeline) | Port harness Claude Code 2.1.302 — **implemented (complete)**: tool spec, prompt blocks, permission modes, compaction v2 (precompute + boundary + idle), subagents (background + send_message), tool search, memory, hooks (20 events), plan mode, in-TUI mode switcher. Chỉ còn per-block cache scopes phía fantasy là ngoài tầm repo. |

## Hướng dẫn delegate cho agent

Khi giao việc, kèm theo prompt mẫu sau (thay `<PLAN>` và `<PHASE>`):

> Đọc file plan `<PLAN>` trong `docs/plans/` và implement theo đúng thứ tự
> phase trong đó. Chỉ làm `<PHASE>` (mặc định: toàn bộ). Tuân thủ:
> - `task fmt` (gofumpt) trước khi commit; `task lint` + `go test ./...`
>   phải sạch.
> - Log message bắt đầu chữ hoa; comment riêng dòng kết thúc bằng dấu chấm.
> - Test: testify `require`, `t.Parallel()`, `t.TempDir()`, mock providers
>   (`config.UseMockProviders` + `config.ResetProviders()`), golden files
>   (`-update`).
> - Semantic commit một dòng (`feat: ...`).
> - Đọc `internal/ui/AGENTS.md` nếu plan đụng tới TUI.
> Báo cáo lại: file đã tạo/sửa, test đã chạy, phần nào chưa xong và vì sao.

## Điểm neo kiến trúc chung (nhắc nhanh)

- Module `github.com/charmbracelet/crush` (fork watcher-cli), Go 1.27,
  `CGO_ENABLED=0`, bubbletea v2, SQLite + sqlc + goose.
- Agent: `internal/agent/coordinator.go` (buildAgent/buildTools/
  buildAgentModels), `agent.go` (SessionAgent/Model), `agent_tool.go`
  (sub-agent `task`), `harness_prompt.go` (điểm inject prompt).
- Config: `internal/config/config.go` (SelectedModelType, SetupAgents,
  Options), crushrc builtins: `internal/shellconfig/register.go`.
- Harness2: `internal/harness/` (Store `.watcher/`, WorktreeManager,
  `llm.go` NewChatAgent/ChatOnce, optimizer).
- CLI: `internal/cmd/` (cobra, đăng ký trong `root.go` init).
- UI: `internal/ui/model/ui.go` (sole model), `dialog/` (overlay),
  `model/keys.go` (keymap — `ctrl+z` đã là suspend), `chat/tools.go`
  (tool renderer factory), `diffview/`, `notification/`.
- Workspace facade: `internal/workspace/app_workspace.go`
  (`AgentRun`, `Subscribe`, ...); events: `app.App.Events/SendEvent`.
- TUI rules bắt buộc: `internal/ui/AGENTS.md`.

## Trạng thái

- [x] 01 — Shadow Git Checkpoints (core service + decorator + `/rewind` dialog)
- [x] 02 — Multi-Model Routing (slot `fast` cho sub-agent task, `model fast`, `--fast-model`)
- [x] 03 — Watchdog Mode (`crush watch`, detector, `--analyze`, `--fix`; TUI popup là phase sau)
- [x] 04 — Tree-sitter Repo Map (tầng A pure-Go: go/parser + fallback regex; inject `<repo-map>` + tool `repo_map`; tầng B CGO là phase sau)
- [ ] 05A — `crush review`
- [ ] 05B — DB/OpenAPI Inspector
- [ ] 05C — Split-Pane Diff Viewer
- [x] 06 — Verify tool + live incremental repo map
- [x] 07 — DeepSeek `/efforts` commands and effort levels
- [ ] 08 — DeepSeek Harness best-practices adoption plan
