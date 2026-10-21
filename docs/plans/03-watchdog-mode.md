# Plan 03 — Watchdog Mode: tự phát hiện lỗi từ log/test và tự sửa

> Ưu tiên: **#3** · Độ khó: ⭐⭐⭐ (Vừa) · Giá trị: Cực cao (đúng chất tên `watcher-cli`) · Ăn khớp harness2: Khớp

## 1. Mục tiêu

`watcher-cli watch <command...>` chạy một lệnh nền (vd: `go test ./...`,
`npm test`, `docker compose logs -f`, `kubectl logs -f pod`) và **canh gác**
output:

1. **Phát hiện bất thường**: regex table cho Go panic/stack trace, test
   FAIL, build error, Node/TS stack, Python traceback, lỗi HTTP 5xx trong
   log, … (mở rộng được qua config).
2. **Trích xuất vị trí lỗi**: parse `file.go:142` từ stack trace (Go:
   `\t/path/file.go:NN +0x..`; chung: `(file):(line)`).
3. **LSP jump-to-code**: dùng `internal/lsp.Manager` để mở file/định nghĩa
   tại dòng lỗi (giàu context cho agent).
4. **Tự phân tích & đề xuất patch**: dùng standalone LLM client
   (`internal/harness/llm.go` pattern) hoặc một agent run để phân tích root
   cause, sinh patch; chế độ `--fix` tự chạy coder agent để sửa.
5. **Thông báo**: desktop notification + (khi chạy trong TUI) popup/dialog:
   *"Phát hiện lỗi ở `service/user.go:142`. Đã chuẩn bị patch — Enter để xem
   diff và áp dụng."*

**Non-goals:** không thay thế CI; không tự push; không theo dõi toàn hệ
thống — chỉ tail output của lệnh user chỉ định.

## 2. Bối cảnh kiến trúc (đã khảo sát)

| Thành phần | Vị trí | Điều quan trọng |
|---|---|---|
| Tail file | `internal/cmd/logs.go` | Đã dùng `github.com/nxadm/tail` (dependency sẵn có) — mẫu tail/follow chuẩn (`followLogs`). Watchdog sẽ tail **stdout/stderr của process con** thay vì file (hoặc cả 2: `--log <file>`). |
| Background shell | `internal/shell/background.go` | `GetBackgroundShellManager()` singleton: `Start(ctx, workingDir, blockFuncs, command, description, maxJobs)`, `GetOutput()`, `Kill()`, `List()`. Watchdog có thể **tái dùng** nó để có jobs list + persist output trong data dir (job_output/job_kill tools của agent cũng đọc được). |
| Standalone LLM | `internal/harness/llm.go` | `NewChatAgent(ctx, store, systemPrompt)` + `ChatOnce(ctx, agent, prompt)` — **dùng lại nguyên xi** cho bước phân tích root cause (không cần viết provider mới). |
| Agent run (auto-fix) | `app.App.RunNonInteractive` / `coordinator.Run` | Pattern headless run trong `internal/cmd/run.go`. Auto-fix: tạo session, `Run` với prompt template chứa log trích xuất + vị trí lỗi. |
| RunComplete | `app.App.RunCompletions()` broker, `notify.RunComplete` | `internal/app/app.go:257`, `internal/agent/notify` — watchdog lắng nghe để biết patch xong chưa. |
| Desktop notification | `internal/ui/notification/` | `notification.go` (native + OSC) — dùng cho thông báo ngoài TUI. |
| TUI dialog/popup | `internal/ui/dialog/` + `Overlay` | Popup "lỗi phát hiện" là một `Dialog` mới; tuân thủ `internal/ui/AGENTS.md` dialog rules. |
| TUI ↔ app events | `app.App.Events(ctx) <-chan pubsub.Event[tea.Msg]`, `App.SendEvent(msg)` | Watchdog (goroutine trong app) publish event → UI mở dialog. Xem `internal/ui/model/ui.go` giant switch để thêm case. |
| LSP | `internal/lsp/manager.go` | `Start(ctx, path)` khởi động server theo file; `Clients()` trả map client; tools `lsp_definition`/`lsp_diagnostics` (`internal/agent/tools/`) là mẫu gọi LSP từ ngoài. |
| CLI | `internal/cmd/root.go` (init đăng ký), `run.go` (pattern flags + subprocess) | Thêm `watchCmd` trong `internal/cmd/watch.go`, đăng ký trong `root.go` init. |
| Config | `config.Options` + crushrc `option` builtin | Thêm block `options.watchdog` (patterns, max_history_lines, auto_fix) nếu muốn cấu hình file — phase 2. |
| Event/telemetry | `internal/event/` | Không bắt buộc. |
| fang | `charm.land/fang/v2` | CLI error/style handler đã dùng trong `Execute()` — command mới tuân theo cobra chuẩn của repo. |

## 3. Thiết kế

### 3.1 Package mới: `internal/watchdog`

```
internal/watchdog/
  watchdog.go        // Watchdog struct + vòng lặp
  detector.go        // regex detection + severity + vị trí
  stacktrace.go      // parse file:line từ Go/Node/Python stack
  watchdog_test.go
  detector_test.go
  stacktrace_test.go
```

```go
type Finding struct {
    Severity  Severity        // error | warning | info
    Kind      string          // "go_test_fail", "go_panic", "node_stack", "generic_error", ...
    Message   string          // dòng/khối log khớp
    Locations []SourceLocation // file:line đã parse
    Context   string          // N dòng log xung quanh (tail), để đưa vào prompt
    At        time.Time
}

type SourceLocation struct {
    File string
    Line int
}

type Detector struct {
    // patterns: []Pattern{Name, *regexp.Regexp, Severity}
    MaxContextLines int
}
func (d *Detector) Scan(line string, seen int64) *Finding
// (giữ ring buffer N dòng cuối để ghép context; stack trace nhiều dòng
//  → state machine nhỏ: khi khớp header "panic:" thì thu thập cho đến dòng
//  hết goroutine)

type Watchdog struct {
    Cmd     []string
    LogFile string          // tùy chọn: tail file thay vì chạy lệnh
    Detector *Detector
    LLM     *llmClient      // bọc harness.NewChatAgent
    OnFinding func(Finding) // hook cho auto-fix / notification
}
func NewWatchdog(...) *Watchdog
func (w *Watchdog) Run(ctx context.Context) error
func (w *Watchdog) Analyze(ctx context.Context, f Finding, workingDir string) (Analysis, error)
// Analyze: prompt template (root-cause analysis) + ChatOnce + parse kết quả
// (json hoặc markdown có fenced diff) → Analysis{PatchDiff, Explanation}
```

- **Detector mặc định** (compile-time regex, có test golden):
  - Go: `panic:`, `goroutine \d+ \[running\]:`, `^\s*FAIL\s`, `--- FAIL:`,
    `^\t(.+\.go):(\d+) \+0x`, `(.+\.go):(\d+):\d+: (.*)` (compile error).
  - Node/TS: `at .* \((.+):(\d+):(\d+)\)`, `\d+ failing`.
  - Python: `Traceback \(most recent call last\)`, `File "(.+)", line (\d+)`.
  - Generic: `(?i)\b(error|fatal|panic|failed)\b` mức warning (để tránh ồn —
    chỉ báo sau khi debounce).
- **Debounce**: cùng file:line + kind trong vòng 5s chỉ báo 1 lần.
- **Vòng lặp**: start process (os/exec, hoặc tail file), đọc từng dòng qua
  `bufio.Scanner` (SetBuffer lớn cho dòng dài), gọi Detector; khi có finding
  mức error → `OnFinding`.

### 3.2 Auto-fix (flag `--fix`)

1. Finding → `Analyze()` (LLM standalone) → `Analysis{PatchDiff}`.
2. Tạo session mới title `watchdog-fix <short>`; chạy
   `coordinator.Run(ctx, sessionID, fixPrompt)` với fixPrompt chứa:
   - finding context (log),
   - vị trí lỗi (file:line),
   - Analysis từ bước 1 (gợi ý patch),
   - yêu cầu: sửa file thật, chạy test để xác nhận.
3. Kết quả: publish `notify.Notification` + desktop notification với tóm tắt
   và đường dẫn file đã sửa.

> Cách 2 (đơn giản hơn, nên làm trước): bỏ qua bước Analyze, gửi thẳng log +
> vị trí vào agent run với prompt template sẵn. Chỉ dùng `ChatOnce` khi muốn
> tiết kiệm chi phí agent loop. **Khuyến nghị: implement Analyze trước (rẻ),
> auto-fix là optional phase 2.**

### 3.3 CLI: `crush watch`

`internal/cmd/watch.go`, đăng ký trong `root.go`:

```
crush watch "go test ./..."
crush watch --log ./app.log                # tail file
crush watch --fix "go test ./..."          # tự sửa khi fail
crush watch --once "go test ./..."         # chạy 1 lần, không lặp
crush watch --interval 30s "go test ./..." # chạy định kỳ (poll)
```

- Chạy headless (không TUI): in finding ra stdout (màu fang/lipgloss), kèm
  patch preview nếu `--fix`/`--analyze`.
- `--interval`: chạy lệnh lặp lại mỗi N giây (watch-mode cho test).

### 3.4 Tích hợp TUI (phase 3, optional)

- Flag mới ở `crush`: `crush --watch "go test ./..."` → sau khi TUI khởi
  động, spawn Watchdog goroutine trong `app.App` (hoặc `UI` model qua
  `tea.Cmd`), finding → `App.SendEvent(WatchdogFindingMsg{...})` → UI mở
  `WatchdogDialog` (Enter xem diff — render bằng `internal/ui/diffview`,
  A áp dụng bằng cách gửi prompt agent).
- Desktop notification qua `internal/ui/notification`.

## 4. Các bước triển khai

### Phase 1 — Core (headless, không LLM)

1. `internal/watchdog/stacktrace.go`: parse file:line cho Go/Node/Python
   (+ test với sample stack traces thật — lưu trong `testdata/`).
2. `internal/watchdog/detector.go`: bảng pattern + ring buffer context +
   debounce (+ test golden).
3. `internal/watchdog/watchdog.go`: Run loop (exec process / tail file),
   OnFinding callback.
4. `internal/cmd/watch.go`: `crush watch` in finding có màu; `--once`;
   `--interval`; `--log`. Đăng ký `root.go`.
5. Test integration nhỏ: chạy script test fail trong `t.TempDir()`.

### Phase 2 — Phân tích & sửa

6. `Watchdog.Analyze` dùng `harness.NewChatAgent` + template prompt
   (`internal/watchdog/analyze.md` embed) → parse patch (fenced diff).
7. `--fix`: tạo session + `coordinator.Run` headless (mẫu `runNonInteractive`
   trong `internal/cmd/run.go`), lắng nghe RunComplete.
8. Desktop notification khi tìm thấy lỗi (dùng `internal/ui/notification`).

### Phase 3 — TUI

9. `WatchdogFindingMsg` + case trong `model/ui.go`; `WatchdogDialog`
   (`internal/ui/dialog/watchdog.go`): hiện finding, preview diff
   (`internal/ui/diffview`), Enter → gửi prompt sửa qua `workspace.AgentRun`,
   Esc → bỏ qua.
10. Flag `crush --watch "cmd"` nối watchdog vào app lifecycle; cleanup khi
    shutdown (kill process con).

## 5. Testing

- Detector/stacktrace: unit + golden (`go test ./internal/watchdog -update`).
- Run loop: dùng lệnh shell giả (`go run ./testdata/failing/main.go` hoặc
  `sh -c 'echo panic: test'`) trong `t.TempDir()`; đóng ctx để dừng.
- Analyze: mock bằng cách đặt provider mock (`config.UseMockProviders`) hoặc
  tách hàm parse patch để test riêng không cần LLM.
- Auto-fix: test với mock provider như `internal/agent` tests.
- TUI: catwalk snapshot dialog.

## 6. Definition of Done

- [ ] `crush watch "go test ./..."` phát hiện test fail + panic, in rõ file:line.
- [ ] Tail file (`--log`) và interval (`--interval`) hoạt động; Ctrl+C dọn process con.
- [ ] `--fix` (nếu implement) tự sửa và thông báo kết quả; không tự push.
- [ ] Detector không báo sai hàng loạt (debounce + severity).
- [ ] `task fmt`, `task lint`, `go test ./...` sạch.

## 7. Rủi ro & giảm thiểu

| Rủi ro | Giảm thiểu |
|---|---|
| Regex nhiễu (false positive) | Phân severity; warning chỉ log, error mới hành động; debounce; config thêm pattern riêng (phase 2). |
| Process con treo / zombie | `exec.CommandContext` + kill process group (xem `internal/shell/exec_*.go` pattern cross-platform); defer cleanup. |
| Token lớn khi log dài | Chỉ gửi N dòng context quanh lỗi (mặc định 200); truncate dòng quá dài. |
| LLM phân tích sai vị trí | Luôn kèm file:line đã parse; agent có tools tự kiểm chứng (view/grep/lsp). |
| Windows signal khác biệt | Dùng pattern sẵn có (`signal.NotifyContext` như run.go; kiểm tra `exec_windows.go`). |

## 8. Quy ước

- Package mới không import TUI (test được độc lập); kết nối TUI qua events.
- Log message bắt đầu chữ hoa; `gofumpt`; test `t.Parallel()` + `t.TempDir()`.
- Commit semantic: `feat: add watchdog mode that detects and fixes errors from logs`.
