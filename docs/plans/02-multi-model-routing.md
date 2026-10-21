# Plan 02 — Multi-Model Routing theo vai trò (Architect / Fast-Explorer / Applier)

> Ưu tiên: **#2** · Độ khó: ⭐⭐ (Dễ) · Giá trị: Cao (giảm ~60% chi phí API, nhanh hơn) · Ăn khớp harness2: Cực khớp

## 1. Mục tiêu

Cho phép mỗi **vai trò** trong hệ thống dùng một model riêng, thay vì tất cả
dùng chung "large model":

| Vai trò | Model đề xuất | Lý do |
|---|---|---|
| **Coder / Applier** (main agent, sửa file, chạy lệnh) | `large` (giữ nguyên) | Cần chất lượng code cao nhất. |
| **Task / Fast-Explorer** (sub-agent của tool `agent`: grep, đọc code, tóm tắt) | **`fast`** (mới) | Chạy nhiều lần, song song (`fantasy.NewParallelAgentTool`); model rẻ/nhanh đủ cho tìm kiếm + tóm tắt. |
| **Plan / Architect** (phân tích sâu, lập kế hoạch trước khi sửa) | `large` (giữ nguyên) | Reasoning cao. |
| **Summarize** (tự động tóm tắt khi hết context) | `small` (giữ nguyên hành vi hiện tại) | Không thay đổi. |

**Non-goals:** không tự động đoán model theo độ khó task (phase 2 sau này);
không thay đổi hành vi khi người dùng chưa cấu hình `fast` (fallback y hệt
hiện tại); không đổi API fantasy.

## 2. Bối cảnh kiến trúc (đã khảo sát)

| Thành phần | Vị trí | Điều quan trọng |
|---|---|---|
| Model types | `internal/config/config.go:55-58` | `SelectedModelTypeLarge = "large"`, `SelectedModelTypeSmall = "small"`; map `Config.Models map[SelectedModelType]SelectedModel`. |
| Agent defs | `internal/config/config.go:1116 SetupAgents()` | 3 agent: `coder`, `task`, `plan` — **cả 3 đang trỏ `Model: SelectedModelTypeLarge`**. `AgentTask`/`AgentPlan` có AllowedTools read-only. |
| Agent struct | `internal/config/config.go` (field `Model SelectedModelType`) | Thêm khả năng trỏ tới `fast` ở đây. |
| Build models | `internal/agent/coordinator.go:976 buildAgentModels(ctx, isSubAgent)` | Đọc `Config().Models[large]` + `[small]`, dựng fantasy provider → `Model{Model, CatwalkCfg, ModelCfg, FlatRate}`. Có `// TODO: when we support multiple agents...` ở dòng 975 — đây chính là chỗ phải sửa. |
| Build agent | `internal/agent/coordinator.go:792 buildAgent(ctx, prompt, agent config.Agent, isSubAgent)` | Gọi `buildAgentModels`; **bỏ qua `agent.Model`** hiện tại. Sub-agent (tool `agent`) được build tại `internal/agent/agent_tool.go:36` với `agentCfg = Config().Agents[AgentTask]`. |
| Chọn model per run | `coordinator.run` → `updateAgentModels` → `agent.Model()` | `updateAgentModels` (dòng 1458) rebuild lại models từ config. |
| Small model usage | `Summarize` trong `internal/agent/agent.go` | Dùng `a.smallModel.Get()` — không đổi. |
| CLI override | `internal/cmd/run.go` `--model`/`--small-model` + `overrideModels` | Thêm `--fast-model` theo cùng pattern (`UpdatePreferredModel(scope, SelectedModelTypeFast, ...)`). |
| Crushrc builtins | `internal/shellconfig/register.go` | `provider`, `model`, `mcp`, `lsp`, `permissions`, `hook`, `option`. Cần thêm builtin `fast_model` (và tùy chọn: `agent` builtin để set model/agent). Xem handler `handleModel` làm mẫu. |
| Config resolve/merge | `internal/config/load.go:169` | `cfg.Models[Large] = resolved.Large; cfg.Models[Small] = resolved.Small` — thêm `Fast`; logic fallback "nếu không set fast → dùng small → dùng large" đặt ở đây hoặc `store.go:1479`. |
| UI models dialog | `internal/ui/dialog/` (models), `internal/cmd/models.go` | Có 2 slot large/small hiện tại; thêm slot fast (hiển thị + set qua `UpdatePreferredModel`). |
| GetDefaultSmallModel | `internal/app/app.go:631` | Mẫu để viết `GetDefaultFastModel` (chọn model rẻ trong catalog provider; heuristic: `mini/flash/haiku/lite` hoặc rẻ nhất theo pricing). |
| Model struct | `internal/agent/agent.go:187` | `Model{Model fantasy.LanguageModel; CatwalkCfg catwalk.Model; ModelCfg config.SelectedModel; FlatRate bool}` — đủ generic, không cần đổi. |
| agenttest | `internal/agent/agenttest/` | Helper test coordinator với mock providers (`config.UseMockProviders` + `config.ResetProviders()`). |
| Model pin/scope | `internal/config/model_pin_test.go`, `ScopeWorkspace/ScopeGlobal` | Fast model phải đi qua đúng scope như large/small (workspace pin, recent models). |

## 3. Thiết kế

### 3.1 Config: thêm model type `fast`

`internal/config/config.go`:

```go
const (
    SelectedModelTypeLarge SelectedModelType = "large"
    SelectedModelTypeSmall SelectedModelType = "small"
    SelectedModelTypeFast  SelectedModelType = "fast"   // MỚI
)
```

- `Config.Models["fast"]` có thể vắng → fallback chain:
  `fast → small → large` (resolve trong `load.go` + `store.go`, ghi rõ comment).
- `SetupAgents()`: `AgentTask.Model = SelectedModelTypeFast` (nếu fast chưa
  được cấu hình, fallback xử lý ở coordinator).
- `RecentModels` + `UpdatePreferredModel` + model pin: thêm nhánh `fast` theo
  đúng pattern của `small` (tìm mọi chỗ switch theo model type).
- `schema.json`: chạy `crush schema`/`go run . schema` để regen.
- Crushrc: builtin mới `fast_model <provider/model>` (mirror `model` handler,
  ghi vào `Models[fast]`). JSON config tương ứng key `"fast"`.

### 3.2 Coordinator: buildAgentModels theo vai trò

Sửa `internal/agent/coordinator.go`:

```go
// buildAgentModels(ctx, isSubAgent) -> buildAgentModels(ctx, agent config.Agent, isSubAgent)
```

- Nếu `agent.Model == SelectedModelTypeFast`: large slot của agent đó lấy
  `Config().Models["fast"]` (fallback small → large); small slot vẫn là
  `Models["small"]` (dùng cho summarize nếu có).
- Xóa TODO dòng 975. Mọi call site cập nhật (`buildAgent`, `updateAgentModels`).
- `isSubAgent` giữ nguyên hành vi (copilot client khác nhau).

Kết quả: sub-agent `task` (tool `agent`) tự động chạy bằng model fast, không
cần đổi `agent_tool.go` (nó đã truyền `agentCfg = Agents[AgentTask]`).

### 3.3 Architect flow (tùy chọn bật/tắt qua config)

Thêm `config.Options.AutoPlan bool` (mặc định `false` để không đổi hành vi):

- Khi bật và prompt của user dài/phức tạp (heuristic: >1 câu hỏi hoặc chứa
  từ khóa refactor/design; phase 1 dùng heuristic đơn giản), coordinator
  chạy **plan agent** (large) trước để tạo plan, chèn plan vào system
  prompt/hidden user message của lượt coder (reuse pattern
  `HiddenUserMessage`/`message.HiddenUserMessage(ctx)`).
- Đừng implement phase này nếu thời gian gấp — routing model là giá trị
  chính; auto-plan là bonus.

### 3.4 CLI + UI

- `crush run --fast-model <model>` (pattern `--small-model`, `overrideModels`).
- `crush model fast <provider/model>` (command `models.go`), dialog models
  thêm slot Fast.
- `GetDefaultFastModel(providerID)` trong `internal/app/app.go`: ưu tiên theo
  tên (`mini`, `flash`, `haiku`, `lite`, `nano`) rồi rẻ nhất theo
  `catwalk.Model` pricing; fallback trả small.

## 4. Các bước triển khai

### Phase 1 — Config nền tảng

1. Thêm `SelectedModelTypeFast` + fallback resolve (`load.go`, `store.go`).
2. `SetupAgents()`: `AgentTask.Model = fast`.
3. Builtin `fast_model` trong `internal/shellconfig/` (đăng ký
   `shell.RegisterBuiltin` + `ConfigBuilder` gate như builtin khác).
4. Test config: fallback chain, load từ crushrc/json, clone không leak
   (xem `clone_test.go`), model pin với fast.

### Phase 2 — Coordinator routing

5. Sửa `buildAgentModels(ctx, agent, isSubAgent)` + call sites; xóa TODO.
6. `updateAgentModels` truyền đúng agent.
7. Test với `config.UseMockProviders`: coder dùng large, sub-agent task dùng
   fast, plan dùng large; fallback fast→small khi chưa set.

### Phase 3 — UX

8. `--fast-model` flag + `overrideModels` mở rộng; `GetDefaultFastModel`.
9. Models dialog + `crush model` hỗ trợ 3 slot.
10. `schema.json` regen; docs `docs/models.md` (cách cấu hình vai trò).

### Phase 4 (optional) — Auto-plan Architect

11. `Options.AutoPlan` + pre-plan flow + test.

## 5. Testing

- Config: `internal/config/` — fallback, merge crushrc, pin, clone.
- Coordinator: `internal/agent/coordinator_test.go` + `agenttest` với mock
  provider; assert model được chọn theo role (mock provider trả tên model
  khác nhau, hoặc assert `Model().ModelCfg.Model`).
- CLI: `run_test.go` pattern cho `--fast-model` parse/validate.
- Chạy tay: set `fast_model` trong crushrc, hỏi một câu cần sub-agent
  (tool `agent`), xác nhận header/usage hiển thị model fast; giá cost giảm.

## 6. Definition of Done

- [ ] `coder`/`plan` chạy large; sub-agent `task` chạy `fast` (fallback small/large khi chưa cấu hình).
- [ ] Cấu hình được từ crushrc (`fast_model`) và `crush.json` (`"fast"`), pin theo scope hoạt động.
- [ ] `crush run --fast-model` + `crush model fast` hoạt động.
- [ ] Không đổi hành vi mặc định (fast chưa set → y hệt hiện tại).
- [ ] `task fmt`, `task lint`, `go test ./...` sạch.

## 7. Rủi ro & giảm thiểu

| Rủi ro | Giảm thiểu |
|---|---|
| Model fast kém → sub-agent trả kết quả tồi | Fallback tường minh; docs khuyên model fast ≥ Haiku/Flash; user vẫn ép `task` dùng large bằng cách set fast = model large. |
| Chỗ nào đó hardcode chỉ 2 model type | Grep toàn repo `SelectedModelTypeLarge|Small` và sửa mọi switch; test clone/pin sẽ lộ. |
| Provider không có model rẻ | `GetDefaultFastModel` fallback small; không crash. |
| Break schema.json | Regen bằng `crush schema` + test schema. |
| Catwalk model catalog thiếu pricing | Fallback theo tên; không phụ thuộc pricing duy nhất. |

## 8. Quy ước

- Giữ comment `// TODO` cũ chỉ bị xóa khi thực sự xử lý xong multi-agent.
- Format `gofumpt`; log chữ hoa đầu dòng; JSON tag snake_case.
- Commit semantic: `feat: add fast model slot and per-role model routing`.
