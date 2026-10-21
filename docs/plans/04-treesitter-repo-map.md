# Plan 04 — Repo Map thông minh: Symbol Graph + Blast Radius (Tree-sitter)

> Ưu tiên: **#4** · Độ khó: ⭐⭐⭐⭐ (Khó) · Giá trị: Cao (hiểu codebase lớn cực nhanh) · Ăn khớp harness2: Khớp

## 1. Mục tiêu

Xây một **repo map** trong bộ nhớ + cache trên đĩa, cho biết:

- Cấu trúc thư mục/file quan trọng (bỏ qua vendor/node_modules/…).
- Symbol-level: package → symbols (func/type/method) → callers/callees,
  imports, `struct X implements interface Y`.
- Query API: `BlastRadius(file hoặc symbol)` → danh sách file bị ảnh hưởng;
  `RelatedFiles(file)`; `Dependencies(module)`.

Khi agent nhận task, một **section `<repo-map>` được tự động đính vào system
prompt** (giống cách `harness_prompt.go` inject harness) để lượt đầu không
phải mò `grep`. Kèm theo một **tool `repo_map`** để agent truy vấn chủ động.

**Non-goals:** không thay thế LSP (dùng chung); không index lịch sử git;
không đảm bảo chính xác 100% ngữ nghĩa (map là gợi ý, luôn kèm cảnh báo).

## 2. Ràng buộc quan trọng nhất: CGO

Repo build với **`CGO_ENABLED=0`** (AGENTS.md). Các binding tree-sitter Go
phổ biến (`github.com/tree-sitter/go-tree-sitter`,
`github.com/smacker/go-tree-sitter`) đều cần CGO.

→ **Thiết kế 2 tầng (bắt buộc theo đúng thứ tự này):**

- **Tầng A (pure Go, mặc định, ship luôn):**
  - Go repos: dùng **stdlib `go/parser` + `go/ast`** (đã có sẵn, zero dep) —
    parse chính xác symbols, calls, imports của repo Go (bản thân watcher-cli
    là Go nên tầng này có giá trị ngay lập tức).
  - Ngôn ngữ khác: index nhẹ theo cấu trúc (fastwalk file inventory + regex
    nhẹ cho `func/def/class/import` — như cách các repo-map đơn giản hoạt
    động), ghi rõ `confidence: low`.
- **Tầng B (CGO, optional, build tag `repomap_cgo`):**
  - `github.com/tree-sitter/go-tree-sitter` + grammar packages (go, ts, py,
    rs, …), file `internal/repomap/cgo/` có build tag; binary release giữ
    `CGO_ENABLED=0`, ai cần chính xác cao tự build với tag.

## 3. Bối cảnh kiến trúc (đã khảo sát)

| Thành phần | Vị trí | Điều quan trọng |
|---|---|---|
| Prompt injection | `internal/agent/harness_prompt.go` `appendHarnessPrompt(systemPrompt, workingDir)` | Gọi tại `coordinator.buildAgent` (`internal/agent/coordinator.go:832`). Repo map chèn **cùng chỗ** (sau harness) qua hàm `appendRepoMapPrompt(...)`. |
| Tool registry | `coordinator.buildTools` (`internal/agent/coordinator.go:849`) + danh sách tên tool `allToolNames()` (`internal/config/config.go:1038`) | Tool mới `repo_map` thêm vào đây; mỗi tool có `.md` mô tả (`internal/agent/tools/`). |
| File walk | `github.com/charlievieth/fastwalk` (dependency sẵn) | Dùng cho inventory nhanh (đã được dùng trong repo — tìm chỗ dùng hiện tại để copy pattern). |
| Watch thay đổi | `github.com/fsnotify/fsnotify` (dependency sẵn) | Incremental reindex (phase 2). |
| LSP | `internal/lsp/` + tools `lsp_call_hierarchy/lsp_definition/lsp_symbols` | Repo map có thể dùng LSP để verify cạnh (call hierarchy) — nhưng không phụ thuộc. |
| Harness store | `internal/harness/store.go` | Cache đặt tại `.watcher/repomap/` (cùng convention `.watcher/`). |
| Config options | `config.Options` + crushrc `option` builtin | Thêm `options.repo_map` (enable, max_symbols, include/exclude globs). |
| Glob patterns | `github.com/bmatcuk/doublestar/v4` (dependency sẵn) | Dùng cho include/exclude. |
| SQLite/sqlc | `internal/db/` | **Không** cần DB — map cache là file JSONL ở `.watcher/repomap/` (đơn giản, dễ debug). |

## 4. Thiết kế

### 4.1 Package mới: `internal/repomap`

```
internal/repomap/
  types.go         // Symbol, File, Graph, Edge, Confidence
  walk.go          // inventory: fastwalk + include/exclude
  goparse.go       // go/parser indexer (pure Go) — tầng A
  fallback.go      // regex nhẹ cho ngôn ngữ khác — tầng A
  graph.go         // SymbolGraph: build/query (BlastRadius, RelatedFiles)
  cache.go         // JSONL cache .watcher/repomap/{graph.jsonl,index.json}
  inject.go        // RenderPromptSection(graph) -> string
  repomap.go       // Service: Index/Build/Load/Query (thread-safe, RWMutex)
  cgo/             // build tag `repomap_cgo` — tầng B (tree-sitter)
    tsindex.go
  *_test.go
```

```go
type Confidence string // high (AST thật) | low (regex)

type Symbol struct {
    ID         string     // "pkg/path.FuncName"
    Name       string
    Kind       string     // func | method | type | struct | interface | const | var
    File       string     // relative path
    Line       int
    Package    string
    Confidence Confidence
}

type Edge struct {
    From string // symbol ID hoặc file
    To   string
    Kind string // calls | imports | implements | embeds | references
}

type Graph struct {
    Files    []FileInfo        // path, size, language, hasTests
    Symbols  map[string]Symbol
    Edges    []Edge
    BuiltAt  time.Time
    Root     string
    ModPath  string            // module path nếu là Go repo (go.mod)
    FileHash map[string]string // để incremental
}

type Service interface {
    // Index quét (hoặc cập nhật incremental) và cache lại.
    Index(ctx context.Context) (*Graph, error)
    // Load trả graph từ cache, hoặc index nếu cache cũ/thiếu.
    Load(ctx context.Context) (*Graph, error)
    // Query: BlastRadius(symbolOrFile, depth), RelatedFiles(file), Callers(calleeID), Implementers(ifaceID)
    Query(g *Graph, q Query) ([]Symbol, error)
    // PromptSection render tóm tắt (bounded, ~6-8KB) cho system prompt.
    PromptSection(g *Graph, focus []string) string
}
```

### 4.2 Blast radius

- `BlastRadius("internal/agent/coordinator.go", depth=2)`:
  - ngược: ai gọi symbols trong file này (callers/importers),
  - xuôi: symbols file này gọi/import,
  - BFS theo `Edges` tới `depth` (mặc định 2) → danh sách file + symbol, sắp
    xếp theo mức độ liên quan.
- `Implementers(ifaceID)`: tập hợp cạnh `implements` (Go: parse
  `var _ Iface = (*T)(nil)` và method-set matching đơn giản).

### 4.3 Inject vào prompt

Trong `internal/agent/coordinator.go` (cạnh `appendHarnessPrompt`):

```go
systemPrompt = appendHarnessPrompt(systemPrompt, c.cfg.WorkingDir())
systemPrompt = appendRepoMapPrompt(systemPrompt, repomapSvc) // MỚI
```

- `appendRepoMapPrompt` gọi `svc.Load(ctx)` (cache) — **không block lâu**:
  nếu cache miss và repo lớn (> 5k files), trả về section rỗng + ghi log,
  index chạy nền (goroutine) để lượt sau có.
- Section dạng:

```
<repo-map>
Root: .
Module: github.com/charmbracelet/crush
Key packages: internal/agent (coordinator, session agent...), internal/ui (TUI), ...
Notable symbols: coordinator.buildTools, sessionAgent.Run, ...
Query me with the repo_map tool for blast radius / callers / implementers.
</repo-map>
```

- Bounded: `maxSymbols` (default 120), `maxSectionBytes` (default 8_000) —
  config `options.repo_map`.

### 4.4 Tool `repo_map`

`internal/agent/tools/repo_map.go` + `repo_map.md`:

- Params: `{query: "blast_radius|related|callers|implementers|summary",
  target: "file.go|pkg.Name|symbol", depth: int}`
- Read-only → thêm vào `resolveReadOnlyTools` + `resolvePlanTools`
  (`internal/config/config.go`) và `allToolNames()`; cho phép sub-agent task
  dùng (giúp Fast-Explorer tiết kiệm vòng grep — ăn khớp Plan 02).
- Trả kết quả dạng markdown ngắn gọn, luôn kèm `confidence`.

### 4.5 Tầng B — tree-sitter (CGO, build tag)

- File `internal/repomap/cgo/tsindex.go` với `//go:build repomap_cgo`.
- Interface chung `indexer` (go | ts | py | rs | fallback) đăng ký theo
  extension; file `_cgo.go` compile khi tag bật, ngược lại chỉ dùng tầng A.
- Tài liệu rõ: `CGO_ENABLED=1 go build -tags repomap_cgo .`.

## 5. Các bước triển khai

### Phase 1 — Tầng A (pure Go) + cache

1. `types.go`, `walk.go` (fastwalk + doublestar include/exclude mặc định:
   bỏ `.git`, `vendor`, `node_modules`, `dist`, `build`, `*.min.js`, …).
2. `goparse.go`: parse mọi `*.go` (bỏ `_test.go` cho graph chính nhưng giữ
   `hasTests` flag): package, funcs, methods, types, interfaces, calls
   (ast.CallExpr → selector), imports, `implements` heuristics.
3. `fallback.go`: regex nhẹ (`^\s*(func|def|class|interface|type)\s+...`) cho
   ts/py/js/rs/java — confidence low.
4. `graph.go`: build + BFS queries.
5. `cache.go`: ghi `.watcher/repomap/` JSONL + file-hash incremental (phase 2).
6. Test: dùng repo fixture nhỏ trong `testdata/` (Go mini-project có call
   chain + interface impl); assert BlastRadius chính xác.

### Phase 2 — Inject prompt + tool

7. `inject.go` render section; wire `appendRepoMapPrompt` trong coordinator
   (sau harness prompt); bật/tắt bằng `options.repo_map.enabled` (default on,
   fallback an toàn khi lỗi).
8. Tool `repo_map` + `.md`; thêm vào `allToolNames`, `resolveReadOnlyTools`,
   `resolvePlanTools`; register trong `buildTools`.
9. Config: `options.repo_map` (enabled, max_symbols, max_section_bytes,
   include, exclude) + `schema.json` regen (`crush schema`).
10. fsnotify incremental reindex (debounce 2s) — optional cuối phase.

### Phase 3 — Tầng B (CGO) + docs

11. `cgo/tsindex.go` build tag; grammar cho go/ts/py; docs build hướng dẫn.
12. `docs/repomap.md` + README mục ngắn.

## 6. Testing

- Indexer Go: fixture repos (`testdata/`) — assert symbols, edges,
  blast radius (depth 1/2), implementers.
- Cache: index 2 lần — lần 2 dùng cache (mock time/file mtime); file đổi →
  reindex đúng file.
- Prompt section: bounded (không vượt max bytes), repo trống/lỗi → không
  panic, không làm chậm agent start.
- Tool: integration với mock provider (agenttest) gọi `repo_map`.
- Benchmark nhẹ: index repo watcher-cli (< 500ms lần đầu, cache hit < 50ms)
  — `BenchmarkIndex`.

## 7. Definition of Done

- [ ] `go test ./...` sạch với `CGO_ENABLED=0` (tầng A đủ chạy không tag).
- [ ] Chạy `crush` trong repo Go → prompt có `<repo-map>`; hỏi agent "sửa hàm
      X" → lượt đầu đã nhắc đúng file liên quan (kiểm chứng bằng tool calls).
- [ ] Tool `repo_map` trả blast radius/callers đúng trên repo watcher-cli.
- [ ] Không làm chậm khởi động agent đáng kể (cache + background index).
- [ ] `task fmt`, `task lint`, benchmark trong ngưỡng.

## 8. Rủi ro & giảm thiểu

| Rủi ro | Giảm thiểu |
|---|---|
| CGO cấm với release build | Tầng A pure-Go là sản phẩm chính; tầng B gắn build tag, không bao giờ vào binary mặc định. |
| Prompt phình → tốn token | Bounded section (8KB) + chỉ liệt kê symbol quan trọng (exported, nhiều cạnh). |
| Parse sai dẫn agent sai đường | `confidence` label rõ; tool trả cảnh báo; map luôn là "gợi ý", agent vẫn có grep/view. |
| Repo lớn index chậm lần đầu | Chạy nền sau khởi động; cache JSONL; incremental bằng file hash + fsnotify. |
| Go generics/embed phức tạp | `go/parser` xử lý sẵn (Go ≥ 1.27 parser); test fixture có generics + go:embed. |

## 9. Quy ước

- Không import CGO code từ package non-tagged (giữ `repomap` biên dịch được
  với `CGO_ENABLED=0`).
- Log chữ hoa đầu dòng; comment kết thúc dấu chấm; `gofumpt`.
- Cache files thuộc `.watcher/repomap/` — không chạm `.git`.
- Commit semantic: `feat: add repo map with symbol graph and blast radius queries`.
