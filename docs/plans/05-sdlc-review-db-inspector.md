# Plan 05 — SDLC tích hợp: Local PR Review + Schema-Aware DB/API Inspector + Diff Viewer tương tác

> Ưu tiên: **#5** · Độ khó: ⭐⭐⭐ (A), ⭐⭐⭐ (B), ⭐⭐⭐⭐ (C) · Giá trị: Cao (đồng hành cả vòng đời phát triển) · Ăn khớp harness2: Khớp (dùng lại `.watcher/memory/lessons.md`)

File này gồm **3 phần độc lập** — có thể delegate riêng từng phần cho một
agent (5A → 5B → 5C theo thứ tự khuyến nghị; 5C không phụ thuộc 5A/5B).

---

## Phần 5A — `crush review`: Local PR / Diff Reviewer

### Mục tiêu

Lệnh mới `crush review` đọc diff (`git diff main...HEAD`, `--staged`, hoặc
working tree), kết hợp **bài học đã học** trong `.watcher/memory/lessons.md`
(từ harness2) + checklist có sẵn, gọi LLM standalone để review, in báo cáo
theo hạng mục (bug, secret, error-handling, concurrency, convention). Flag
`--fix` để giao cho coder agent tự sửa các phát hiện mức cao.

### Bối cảnh kiến trúc

- **Standalone LLM**: `internal/harness/llm.go` — `NewChatAgent(ctx, store,
  systemPrompt)` + `ChatOnce(ctx, agent, prompt)` → text. Review dùng nguyên
  xi (cùng pattern editor của harness).
- **Lessons**: `internal/harness/store.go` — `NewStore(workingDir)` +
  `LoadLessons()` trả markdown lessons (rỗng nếu chưa có → review vẫn chạy
  với checklist mặc định).
- **Git diff**: pattern `runGitOut` trong `internal/harness/worktree.go`
  (shell-out `git diff` với `cmd.Dir`); hoặc `gitutil` nếu có helper phù hợp
  (kiểm tra `internal/gitutil/`).
- **CLI**: cobra trong `internal/cmd/`, đăng ký `root.go` init (như `runCmd`).
  Flag: `--base main`, `--staged`, `--fix`, `--format {pretty|json|markdown}`.
- **Headless agent fix**: `--fix` đi theo pattern `crush run` non-interactive
  (`internal/cmd/run.go`): tạo session, `SendMessage`/`AgentRun`, lắng nghe
  `RunComplete`.

### Thiết kế

```
internal/review/
  review.go        // ReviewService: BuildPrompt(diff, lessons, checklist) + Run + Parse
  checklist.go     // các rule tĩnh: secret regex (AKIA, sk-, password=, ...),
                   //   defer Close, err check, goroutine leak heuristics
  review_test.go
```

- `checklist.go` quét tĩnh (không LLM) → luôn chạy trước, rẻ, chính xác 100%
  cho secret/pattern cơ bản.
- Prompt LLM: `<lessons>` (nếu có) + `<diff>` (bounded ~32KB, diff lớn thì cắt
  theo file) + yêu cầu trả JSON `[{file,line,severity,category,message,
  suggestion}]` → parse bằng `encoding/json` (fallback markdown nếu LLM lệch
  format — parser dung sai).
- Output: bảng màu (fang/lipgloss — xem `internal/format/` hoặc style của
  `run.go` spinner), nhóm theo severity; `--format json` cho CI.
- `--fix`: gom findings severity ≥ medium thành prompt cho coder agent.

### Các bước

1. `internal/review/checklist.go` + tests (fixture diff chứa secret, missing
   error check).
2. `internal/review/review.go` prompt + parse + tests (parse JSON + fallback).
3. `internal/cmd/review.go` + đăng ký root; `--base/--staged/--fix/--format`.
4. Docs `docs/review.md` + mục README ngắn.

### DoD 5A

- `crush review` trên chính repo này in báo cáo có cấu trúc; diff rỗng →
  thông báo "nothing to review".
- Secret/pattern tĩnh bắt được **không cần** LLM (test offline).
- Không crash khi chưa có lessons, chưa cấu hình provider (báo lỗi rõ).

---

## Phần 5B — Schema-Aware Inspector: DB, Redis, OpenAPI

### Mục tiêu

Thêm các **built-in tool đọc (read-only)** để agent tra cứu schema thật trước
khi viết SQL/struct — chống hallucination tên cột:

- `db_schema`: liệt kê bảng/cột/khóa (information_schema) của PostgreSQL/MySQL.
- `db_query`: chạy query **chỉ đọc** (SELECT/SHOW/DESCRIBE/EXPLAIN).
- `openapi_spec`: đọc OpenAPI/Swagger (yaml/json) → tóm tắt endpoints,
  request/response schema cho agent.
- (Tùy chọn cuối) `redis_scan`, `kafka_topics` — đánh dấu phase 4.

### Bối cảnh kiến trúc

- **Tool interface**: `internal/agent/tools/` — mỗi tool `.go` + `.md`;
  register trong `coordinator.buildTools` (`internal/agent/coordinator.go:849`),
  tên tool vào `allToolNames()` (`internal/config/config.go:1038`), thêm vào
  `resolveReadOnlyTools` (cho task/plan agent), permission qua
  `permission.Service` (mẫu: `tools.NewEditTool(c.permissions, ...)`).
- **Config**: crushrc builtins `internal/shellconfig/register.go` — thêm
  builtin `db` (VD: `db postgres "postgres://user:pass@host/db?sslmode=..."`)
  hoặc tái dùng `provider`-style; ghi vào `config.Options`/struct mới
  `config.Databases []DatabaseConfig` + regen `schema.json`.
- **Dependencies cần thêm** (go.mod): driver thuần Go
  `github.com/jackc/pgx/v5/stdlib` (PostgreSQL) và
  `github.com/go-sql-driver/mysql`; đều build được `CGO_ENABLED=0`.
- **YAML**: `gopkg.in/yaml.v3` đã có trong go.mod (dùng cho OpenAPI).

### Thiết kế

```
internal/schemainspector/        // logic thuần, không biết agent
  config.go     // DatabaseConfig{Name, Type, URL, ReadOnly}
  pg.go         // introspect postgres (information_schema + pg_catalog)
  mysql.go      // introspect mysql
  guard.go      // ReadOnlyGuard: allowlist SELECT/WITH/SHOW/DESCRIBE/EXPLAIN,
                //   chặn multi-statement (;), chặn hàm ghi, LIMIT tự thêm (max 200 rows)
  openapi.go    // load + tóm tắt spec
  *_test.go
internal/agent/tools/
  db_schema.go + db_schema.md
  db_query.go  + db_query.md
  openapi_spec.go + openapi_spec.md
```

- `guard.go`: phân tích query đơn giản (regex/prefix + đếm `;` ngoài string);
  chỉ cho query bắt đầu SELECT/WITH/SHOW/DESCRIBE/EXPLAIN; **never** DDL/DML;
  giới hạn rows + thời gian (context timeout 5s). Đây là phần phải test kỹ
  nhất (SQL injection không phải mối lo — nhưng tool chỉ đọc là cam kết).
- `db_schema` output: markdown bảng `table | column | type | pk | fk` —
  agent dùng trực tiếp để viết SQL/struct.
- `openapi_spec`: tham số path/query + schema summary mỗi endpoint (giới hạn
  độ dài), cache theo file mtime.
- Permission: mặc định tool mới = cần hỏi (như bash); khi yolo thì chạy.
- UI chat renderer: thêm vào `internal/ui/chat/` (bảng render đơn giản —
  mẫu `chat/search.go`/`chat/generic.go`), đăng ký trong `NewToolMessageItem`
  (`internal/ui/chat/tools.go`).

### Các bước

1. `internal/schemainspector/` + guard tests (block UPDATE/DELETE/multi-stmt,
   allow SELECT/WITH, timeout, row limit).
2. Introspection pg/mysql + tests với DB thật qua `dockertest`? — **không**:
   test bằng `t.SetEnv` + mock driver (interface `DBQuerier`) để offline;
   e2e có DB thật chỉ trong CI optional (gha).
3. Config `db` builtin + `DatabaseConfig` + schema regen.
4. Tools `db_schema`/`db_query`/`openapi_spec` + register + permission +
   chat renderer.
5. Docs `docs/inspector.md`.

### DoD 5B

- `db_query` **không thể** chạy câu lệnh ghi bằng mọi trick test (chữ hoa,
  comment lồng, multi-statement, CTE ghi).
- Agent hỏi "viết query lấy danh sách đơn hàng" → tool trả schema thật, agent
  viết đúng tên cột (kiểm chứng trên DB test).
- OpenAPI loader đọc được spec thật (fixture petstore) không cần network.

---

## Phần 5C — TUI Split-Pane Diff Viewer tương tác (y/n/c per hunk)

### Mục tiêu

Khung xem diff **side-by-side** cạnh khung chat (hoặc dialog toàn màn) cho
các thay đổi agent vừa tạo: `y` chấp nhận hunk, `n` từ chối (revert hunk),
`c` comment vào dòng → gửi yêu cầu sửa đúng dòng đó cho agent.

### Bối cảnh kiến trúc

- **Diff rendering sẵn có**: `internal/ui/diffview/` (unified + split với
  syntax highlight) — **đọc kỹ API trước khi viết**; và `internal/diff/`
  (types diff; go-udiff là dependency).
- **Tool renderers**: `internal/ui/chat/file.go` render Edit/Write/MultiEdit
  (đã hiển thị diff inline) — điểm mở rộng: thêm nút/key mở chế độ review.
- **Dialog**: interface trong `internal/ui/dialog/dialog.go` + overlay stack;
  tuân thủ "Dialog rendering rules" (`internal/ui/AGENTS.md`).
- **Gửi follow-up**: `workspace.AgentRun(ctx, sessionID, prompt)` —
  `internal/workspace/app_workspace.go:111`; comment dòng → prompt dạng
  "Ở file X dòng Y (hunk Z): <comment>. Sửa lại đúng chỗ này."
- **Revert hunk**: dùng diff của hunk để `git apply -R` (pattern
  `internal/harness/worktree.go` `runGitStdin`) — hoặc qua shadow-git
  checkpoint (Plan 01) nếu đã có.
- **Keymap**: thêm binding vào `internal/ui/model/keys.go` (`Chat` group).

### Thiết kế

- `DiffReviewState`: tập hợp các `Hunk{File, Diff, Accepted, Comment}` thu
  từ tool results của `edit/write/multiedit` trong session hiện tại (đọc
  `message.ToolResult`/`ToolCall` từ message service).
- `DiffReviewDialog` (full-screen): 2 pane (cũ/mới) qua `diffview`, con trỏ
  hunk, status bar (`y/n/c/esc`, "accepted 3/7").
- `n` → `git apply -R` hunk đó (hoặc restore file từ checkpoint — ưu tiên
  cách này nếu Plan 01 đã merged, an toàn hơn).
- `c` → input nhỏ (textarea) → đóng dialog → `AgentRun` follow-up.
- `y` → đánh dấu accepted (không làm gì thêm — code đã được apply).
- Mở từ: (1) phím mới `ctrl+d` xung đột (đã là "toggle details") → dùng
  `ctrl+shift+d` hoặc command `/diff`; (2) focus tool message Edit/Write rồi
  bấm `e` (expand hiện có) giữ nguyên, thêm key riêng.

### Các bước

1. Nghiên cứu `internal/ui/diffview/` + `internal/diff/` → wrapper
   `diffview.View(hunks, active)`.
2. Collector: quét messages session → hunks (test unit).
3. `DiffReviewDialog` + route trong `model/ui.go`; keymap + `/diff` command.
4. Hành động revert (git apply -R / checkpoint) + comment follow-up.
5. Catwalk snapshot + docs.

### DoD 5C

- Mở được diff review cho các edit gần nhất; điều hướng hunk bằng phím.
- `n` revert đúng hunk (file khác không đổi); `c` gửi follow-up đúng session;
  `y` đánh dấu.
- Không crash khi diff rỗng/hunk không apply được (báo lỗi rõ).

---

## Rủi ro chung & giảm thiểu

| Rủi ro | Giảm thiểu |
|---|---|
| 5B: tool DB bị lạm dụng ghi dữ liệu | Guard read-only + permission hỏi + docs cảnh báo; không cấp quyền mặc định ngoài SELECT. |
| 5A: LLM review ảo tưởng | Static checklist luôn chạy; báo cáo tách "static (chắc chắn)" vs "LLM (cần người xác nhận)". |
| 5C: revert phá file user đang sửa | Ưu tiên dùng checkpoint (Plan 01); nếu chưa có thì `git apply -R` chỉ cho hunk agent vừa tạo và kiểm tra apply thành công. |
| Thêm driver DB tăng binary | Chấp nhận (driver thuần Go); có thể để sau build tag `inspector` nếu kích thước là vấn đề (phase 4). |

## Quy ước

- Tool mới: `.go` + `.md` đủ cặp; tên tool vào `allToolNames()`; test có
  `t.Parallel()`, `t.TempDir()`.
- Config mới phải regen `schema.json` (`go run . schema`).
- Log chữ hoa đầu dòng, `gofumpt`, semantic commits:
  `feat: add local diff reviewer command`, `feat: add read-only database schema inspector tools`,
  `feat: add interactive hunk review dialog`.
