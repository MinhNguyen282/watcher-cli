# Plan 01 — Shadow Git Checkpoints & Time-Travel (Rewind/Fork)

> Ưu tiên: **#1** · Độ khó: ⭐⭐ (Dễ) · Giá trị: Rất cao · Ăn khớp harness2: Cực khớp

## 1. Mục tiêu

Mỗi lần agent thực thi tool sửa file (`edit`, `multiedit`, `write`, `bash`,
`download`, `lsp_rename`, `lsp_replace_symbol`), `watcher-cli` âm thầm tạo một
**checkpoint** vào một repo git ẩn tại `.watcher/shadow-git/` (tách biệt hoàn
toàn với `.git` chính). Người dùng có thể:

- Gõ lệnh `/rewind` (hoặc phím `ctrl+shift+z`) để mở dialog duyệt danh sách
  tin nhắn/checkpoint theo dòng thời gian.
- Chọn một checkpoint → **toàn bộ file trong working tree được phục hồi về
  đúng trạng thái tại thời điểm đó** (time-travel), không làm bẩn lịch sử
  commit của repo chính.
- **Fork session**: tạo một session con mới bắt đầu từ checkpoint đó để thử
  hướng đi khác mà không phá session gốc.

**Non-goals:** không thay thế git thật; không snapshot file ngoài working
tree; không chặn các tool chạy (snapshot phải < 100ms với repo trung bình).

## 2. Bối cảnh kiến trúc (đã khảo sát)

| Thành phần | Vị trí | Điều quan trọng |
|---|---|---|
| Harness store `.watcher/` | `internal/harness/store.go` | `DirName = ".watcher"`, mẫu persistence chuẩn: `writeFile(rel, data, 0o644)`, `sanitizeFileName`. Shadow-git đặt ở `.watcher/shadow-git/` để chung hệ sinh thái. |
| Git qua CLI | `internal/harness/worktree.go` | Mẫu chuẩn: `runGit`, `runGitOut`, `runGitStdin` — chạy `git` qua `exec.CommandContext` với `cmd.Dir = repoDir`. Shadow-git nên copy pattern này (không cần go-git). |
| Tools sửa file | `internal/agent/tools/{edit,multiedit,write,bash,download}.go`, `lsp_rename.go`, `lsp_replace_symbol.go` | Tất cả được build trong `coordinator.buildTools` (`internal/agent/coordinator.go:849`). Mỗi tool có `.md` mô tả riêng. |
| Decorator tools | `internal/agent/hooked_tool.go`, `wrapToolsWithHooks` | **Điểm chặn lý tưởng**: một decorator `checkpointTool` bọc các tool sửa file, chạy snapshot **trước khi** tool thực thi (trạng thái trước tool = "trạng thái tại tin nhắn trước tool call"). |
| Context keys | `internal/agent/tools/context_test.go` / `tools.go` | `SessionIDContextKey`, `MessageIDContextKey` được set trong `PrepareStep` (`internal/agent/agent.go:932`), có sẵn trong tool execution context → checkpoint biết sessionID + messageID + toolCallID. |
| Session/Message | `internal/session/session.go`, `internal/message/message.go` | `Service.CreateTaskSession(toolCallID, parentSessionID, title)` + `CreateAgentToolSessionID/ParseAgentToolSessionID/IsAgentToolSession` — pattern session-con có ParentSessionID, dùng cho fork. `Message` có `SessionID`, `Parts []ContentPart` (`ToolCall`, `ToolResult`, …). |
| DB | `internal/db/migrations/` (goose), `internal/db/sql/` (sqlc) | Tên migration theo format `YYYYMMDDHHMMSS_ten.sql`. Table `messages` đã có `id, session_id, role, parts(JSON), created_at, updated_at`. |
| File tracker | `internal/filetracker/service.go` | Chỉ track **file đã đọc** (bảng `file_reads`), không lưu nội dung → không dùng được cho snapshot; đừng nhầm. |
| TUI keymap | `internal/ui/model/keys.go` | `ctrl+z` **đã là Suspend** → rewind dùng `ctrl+shift+z`. Key binding mới thêm vào `KeyMap` (struct `Chat`) + `DefaultKeyMap()`. |
| TUI dialog | `internal/ui/dialog/dialog.go` | Interface `Dialog{ID() string; HandleMsg(tea.Msg) Action; Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor}`; quản lý bằng `dialog.Overlay` (`Push/Pop/ContainsDialog`). Xem dialog có sẵn (sessions, models, permissions) làm mẫu + tuân thủ `internal/ui/AGENTS.md` (đặc biệt mục "Dialog rendering rules"). |
| Slash commands | `internal/ui/completions/`, editor `/` | `/rewind` cần đăng ký trong danh sách command (xem cách `/models`, `/sessions` được khai báo ở `internal/ui/model/`). |
| Gửi lệnh cho agent từ UI | `workspace.Workspace.AgentRun(ctx, sessionID, prompt, attachments...)` | `internal/workspace/app_workspace.go:111`. Fork xong có thể gửi prompt tiếp theo qua đây. |
| Pub/sub UI | `app.App.Events(ctx) <-chan pubsub.Event[tea.Msg]`, `App.SendEvent(msg)` | `internal/app/app.go:239,244`. Checkpoint mới có thể publish một event để UI render badge nếu muốn (phase 2). |

## 3. Thiết kế

### 3.1 Package mới: `internal/shadowgit`

```
internal/shadowgit/
  shadowgit.go        // Service interface + core logic
  shadowgit_test.go
```

```go
type Checkpoint struct {
    ID        string    // checkpoint id (ulid/uuid ngắn, dùng làm git branch name)
    SessionID string
    MessageID string    // assistant message chứa tool call ("" nếu snapshot thủ công)
    ToolCallID string   // tool call id ("" nếu snapshot thủ công)
    GitCommit string    // commit SHA trong shadow repo
    Files     []string  // file thay đổi so với checkpoint trước (best effort, từ `git diff --name-only`)
    CreatedAt time.Time
}

type Service interface {
    // Init khởi tạo `.watcher/shadow-git/` (git init) + đảm bảo `.watcher/`
    // nằm trong `.gitignore` của repo chính (tạo nếu chưa có, append dòng
    // `.watcher/` — hỏi ý người dùng qua log, không ép).
    Init(ctx context.Context) error

    // Snapshot ghi trạng thái hiện tại vào shadow repo và trả về checkpoint.
    // No-op nếu working tree không đổi so với checkpoint trước (so `git status --porcelain`).
    Snapshot(ctx context.Context, meta CheckpointMeta) (Checkpoint, error)

    // Restore khôi phục các file đã track trong shadow repo về commit tương ứng.
    // Safe path: nếu tree bẩn → stash tự động (record stash sha vào kết quả để hoàn tác).
    Restore(ctx context.Context, sessionID, checkpointID string) (RestoreResult, error)

    // List trả các checkpoint của session theo thứ tự thời gian.
    List(ctx context.Context, sessionID string) ([]Checkpoint, error)

    // Fork tạo một nhánh mới trong shadow repo từ checkpoint (cho session mới).
    Fork(ctx context.Context, sessionID, checkpointID string) (string, error)

    // Prune xóa checkpoint cũ (giữ N gần nhất, mặc định 50) khi session bị xóa.
    Prune(ctx context.Context, sessionID string, keep int) error

    // Close dọn dẹp.
    Close() error
}
```

### 3.2 Lưu trữ

- **Shadow repo**: `git init` tại `.watcher/shadow-git/`, commit bằng
  `git -C <workingDir> --git-dir=.watcher/shadow-git/.git --work-tree=. add -A`
  rồi `commit`. Điều này khiến mỗi checkpoint là một commit thật (diff được,
  log được) nhưng hoàn toàn vô hình với repo chính.
  - Lưu ý: `--work-tree=.` nghĩa là `.gitignore` của repo chính **vẫn được tôn
    trọng** → không bao giờ snapshot build artifacts, secrets, hay chính
    `.watcher/` (do đã bị ignore). Đây là tính năng, không phải bug.
  - Chạy bằng shell-out `git` giống `internal/harness/worktree.go` (không cần
    thêm dependency).
- **Metadata**: bảng SQLite mới (migration mới `2026MMDDHHMMSS_add_checkpoints.sql`):

```sql
CREATE TABLE IF NOT EXISTS checkpoints (
    id          TEXT PRIMARY KEY,
    session_id  TEXT NOT NULL,
    message_id  TEXT,
    tool_call_id TEXT,
    git_commit  TEXT NOT NULL,
    files       TEXT NOT NULL DEFAULT '[]',
    created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_checkpoints_session ON checkpoints (session_id, created_at);
```

  Query sqlc trong `internal/db/sql/checkpoints.sql` + codegen (theo `sqlc.yaml`).
  Service DB là `checkpointRepo` riêng, inject qua `app.New` giống
  `filetracker.NewService(q)`.

### 3.3 Điểm chặn snapshot

Trong `internal/agent/coordinator.go`, sau `wrapToolsWithHooks` (dòng ~970):

```go
filteredTools = wrapToolsWithHooks(filteredTools, hookRunner, isSubAgent)
filteredTools = shadowgit.WrapMutationTools(filteredTools, svc, c.permissions)
```

- `WrapMutationTools` bọc theo tên tool trong danh sách cố định
  `{edit, multiedit, write, bash, download, lsp_rename, lsp_replace_symbol}`.
- Decorator: trước khi gọi tool gốc → `svc.Snapshot(ctx, meta)` với
  `sessionID/toolCallID` lấy từ context keys; nếu lỗi → **log warning và chạy
  tool như bình thường** (snapshot là best-effort, không bao giờ chặn tool).
- `IsSubAgent`: chỉ snapshot ở coder (không bọc sub-agent task tool để tránh
  N× commit khi sub-agent chạy song song; sub-agent của `agent` tool chạy
  trên cùng working dir nên commit của nó cũng phản ánh trong snapshot coder
  kế tiếp — chấp nhận được).
- Service được khởi tạo lười (lazy init) trong `NewCoordinator`; nếu working
  dir không phải git repo → disable toàn bộ feature (không lỗi).

### 3.4 UI: `/rewind` command + RewindDialog

- **Key**: thêm `Rewind key.Binding` (`ctrl+shift+z`) vào `KeyMap.Chat`.
- **Command**: `/rewind` trong editor → mở `RewindDialog`.
- **`RewindDialog`** (`internal/ui/dialog/rewind.go`):
  - Danh sách các tin nhắn có checkpoint (dùng `list.List` như session dialog).
  - `↑/↓` chọn, `enter` → Restore, `f` → Fork, `esc` đóng.
  - Preview khác biệt: hiển thị `git diff --stat` giữa checkpoint chọn và
    hiện tại (gọi qua service, render bằng `internal/ui/diffview` có sẵn).
  - Tuân thủ "Dialog rendering rules" trong `internal/ui/AGENTS.md`
    (innerWidth, Padding không Margin, `renderDialogHelp`, ...).
- **Fork flow**: `session.Service.CreateTaskSession("", parentSessionID, title)`
  (reuse pattern sub-agent session) → ghi marker message "Forked from
  checkpoint X" vào session mới → đổi session hiện tại (như `SessionDialog`).
  Không cần copy messages: session mới bắt đầu trống, trạng thái file đã
  được restore về checkpoint.
- Sau khi restore/fork, toast nhỏ báo kết quả (dùng `status`/notification
  hiện có trong `internal/ui/model/status.go`).

### 3.5 Tích hợp harness2 (tùy chọn, phase 3)

- `Probe` của harness dùng worktree riêng → không cần shadow-git.
- Tái sử dụng: `RestoreResult.StashRef` cho phép "rewind rồi quay lại" — cùng
  khái niệm `ApplyDiff` của `WorktreeManager`.
- Khi session bị xóa (`session.Service.Delete`), gọi `Prune` để dọn shadow.

## 4. Các bước triển khai

### Phase 1 — Core service (không UI)

1. Migration `add_checkpoints.sql` + `internal/db/sql/checkpoints.sql` + chạy
   `sqlc generate` (xem `Taskfile.yaml`/`sqlc.yaml`).
2. `internal/shadowgit/shadowgit.go`: Init/Snapshot/Restore/List/Fork/Prune.
   - Init: kiểm tra git repo (giống `harness.NewWorktreeManager`), `git init`
     nếu chưa có, append `.watcher/` vào `.gitignore`.
   - Snapshot: `git status --porcelain`; nếu sạch → trả checkpoint rỗng (no-op).
     Commit message: `checkpoint <id> [session=<sid>] [tool=<name>]`.
   - Restore: nếu `git status --porcelain` bẩn → `git stash push -m
     "shadowgit-auto"` (nhớ SHA) → `git checkout <commit> -- .` → trả
     `RestoreResult{RestoredFiles, StashRef}`. Không chạm untracked (đúng chuẩn).
   - Fork: `git branch shadow-fork-<id> <commit>` (chỉ metadata).
3. Unit tests: dùng `t.TempDir()` tạo repo git thật; testify `require`;
   `t.Parallel()`. Test: snapshot idempotent, restore có stash, ignore
   `.watcher/`, Prune giữ N gần nhất, non-git repo → disabled.
4. Decorator `WrapMutationTools` + test: tool được bọc vẫn trả đúng output;
   snapshot lỗi không làm tool fail.
5. Wire vào `NewCoordinator` (sau `wrapToolsWithHooks`).

### Phase 2 — TUI

6. `internal/ui/dialog/rewind.go`: dialog + hành động Restore/Fork/Preview.
7. Keymap `ctrl+shift+z`, command `/rewind`, route trong `model/ui.go`
   (giant switch msg) + `dialog.Overlay`.
8. Sau restore → hiện diff stat + toast. Fork → tạo session con và chuyển.
9. Test: catwalk snapshot cho dialog (theo `internal/ui` chuẩn, chạy
   `go test ./internal/ui/... -update` khi cần).

### Phase 3 — Hoàn thiện

10. Prune khi delete session; giới hạn số checkpoint/session (config option
    `options.shadow_git_max_checkpoints` trong `config.Options` + `schema.json`
    regen bằng `crush schema`).
11. Docs: `docs/shadow-git.md` + một mục ngắn trong README.

## 5. Testing

- `go test ./internal/shadowgit/...` — core, không cần mock provider.
- Decorator test trong `internal/agent/` (xem `agenttest/` làm mẫu).
- Migration test chạy tự động qua goose (pattern sẵn có trong repo).
- TUI snapshot: `charm.land/catwalk` (xem test dialog khác).
- Chạy tay: `go run . ` → chat → edit file → `/rewind` → chọn checkpoint →
  xác nhận file quay về trạng thái cũ; fork tạo session mới.

## 6. Definition of Done

- [ ] Mỗi tool sửa file của coder tạo đúng 1 checkpoint (không trùng lặp khi tree sạch).
- [ ] `.watcher/` nằm trong `.gitignore`, `git status` repo chính không đổi.
- [ ] `/rewind` + `ctrl+shift+z` hoạt động; restore chính xác; tree bẩn được stash an toàn.
- [ ] Fork session hoạt động (session con, restore đúng file).
- [ ] Snapshot chậm/loi không bao giờ chặn tool của agent.
- [ ] Lint sạch: `task fmt`, `task lint`, `task lint:log`, `go test ./...`.

## 7. Rủi ro & giảm thiểu

| Rủi ro | Giảm thiểu |
|---|---|
| Shadow repo phình to (binary, node_modules) | Tôn trọng `.gitignore` repo chính; thêm `.watcher/shadow-git/.git/info/exclude` chặn `.watcher/**`; Prune định kỳ. |
| Snapshot làm chậm agent loop | Chỉ chạy khi `git status` có thay đổi; commit nhanh với repo cỡ vừa; đo benchmark trong test. |
| `ctrl+shift+z` không được terminal truyền | Giữ lệnh `/rewind` làm đường chính; key chỉ là shortcut. |
| Shadow repo bị repo chính nhìn thấy | Append `.watcher/` vào `.gitignore` lúc Init; kiểm tra lại trước mỗi snapshot. |
| Concurrent sessions cùng working dir | Chấp nhận last-write-wins (như git thật); ghi sessionID vào commit message để trace. |

## 8. Quy ước

- Format: `gofumpt` (`task fmt`); log message **bắt đầu chữ hoa**; comment
  riêng dòng kết thúc bằng dấu chấm; file mode octal (0o644/0o755).
- JSON tag snake_case. Lỗi wrap bằng `fmt.Errorf("...: %w", err)`.
- Commit semantic (`feat:` — VD `feat: add shadow git checkpoints and /rewind`).
- Không đụng `.git` chính; chỉ thêm file dưới `.watcher/`.
