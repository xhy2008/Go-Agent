# Go-Codegraph 集成实现计划

> 目标：用纯 Go 重写 codegraph（符号索引 + 代码图查询），作为 go-agent 的可调用工具，
> 并在每次 Agent 完成一轮任务后自动重建索引。
> 旧版 PLAN.md（Fyne 时代的原始设计稿）已过时，本文件为当前唯一实现计划。

---

## 1. 目标与范围

### 1.1 目标

- 新增内部包 `internal/codegraph`：tree-sitter 多语言解析 + SQLite 持久化 + FTS5 全文检索。
- 新增 Agent 可调用工具集（6 个细粒度工具）：`search`/`node`/`callers`/`callees`/`trace`/`impact`，对齐原版 codegraph 的图遍历能力。
- 每次 Agent 完成一轮任务（成功、中断或出错）后，后台自动增量重建索引，保证查询反映最新代码。

### 1.2 范围界定（v1 → v2）

| 维度 | 范围 | 说明 |
| :--- | :--- | :--- |
| 语言 | Go + 主流 15 种（统一 tree-sitter） | **全部语言（含 Go）统一走 tree-sitter**（`tslangs` 通用提取器，一套 `decls` 声明映射表驱动，仅按语法节点差异少量适配）；移除 go/ast、go/types 分流；冷门语言（COBOL/ArkTS/CFML 等）暂未覆盖，注册表可扩展 |
| 存储 | SQLite（v2） | `<项目根>/.codegraph/codegraph.db`（nodes/edges/files/vecs/meta 表 + **contentless FTS5** 虚拟表 nodes_fts）；单连接 + 跨进程互斥锁；符号向量以二进制 blob 持久化；**移除 gob** |
| 全文检索 | SQLite FTS5（v2） | `Search(query, limit)` 执行 `nodes_fts MATCH ? ORDER BY rank`，替代旧的内存词法打分 |
| 语义检索 | 词法 + 可选本地 embedding（v2 改 DLL） | 可选接入 llama.cpp（**DLL 动态链接** `llama_bridge.dll`，syscall.NewLazyDLL 按需加载，带 Vulkan 加速/CPU 回退）加载 GGUF embedding 模型（默认 nomic-embed-text-v1.5，768 维）；**仅配置 `embedding.model`（config.json，`EMBED_MODEL` 可覆盖）时加载 DLL**；查询时 FTS5 候选 → 语义重排；未配置模型时**完全不加载 DLL**，回退原版 codegraph 的 FTS5 全文检索方案 |
| 调用链 | 深度图遍历（v2） | `CallersOf(id, depth)`（入边 BFS）、`Trace(id, depth)`（双向 BFS）、`BlastRadius(id, depth)`（出边 BFS）；BFS 按前进方向节点去重 |
| 索引位置 | `<项目根>/.codegraph/` | 与文件工具一致以进程工作目录为项目根；`.codegraph/` 已 gitignore |
| 触发时机 | Agent.Run 结束后台重建 | 增量（mtime+sha256 指纹），变更文件才重解析 |

---

## 2. 复杂度评估

**总体评级：中偏大**。约 2000–2500 行 Go + 测试，核心难点不在"量"而在"解析正确性"。

### 2.1 模块拆解

| 模块 | 职责 | 估算行数 | 风险 |
| :--- | :--- | :--- | :--- |
| model.go | Node/Edge/Index 结构（无 gob） | ~100 | 低 |
| parse_ts.go | 全部语言统一 tree-sitter 通用提取器 + sourceExts | ~200 | 低 |
| resolve.go | 包聚合、Build（词法 resolveRef + 接口签名匹配 impl 边） | ~300 | 中 |
| index.go | 扫描/构建/增量/落盘/加载（SQLite） | ~400 | 中（增量正确性、文件锁） |
| db.go | SQLite 存储层 + FTS5 全文检索 | ~250 | 中 |
| query.go | MatchByID、CallersOf、Trace（双向 BFS）、BlastRadius | ~250 | 中（BFS 去重方向） |
| embed/ | llama_bridge.dll 动态加载（syscall） | ~150 | 中（Windows ABI） |
| semantic/ | FTS5 候选语义重排 | ~150 | 低 |
| 工具/接线 | 6 细粒度工具 + 自动重建钩子 | ~250 | 低 |
| 测试 | golden 样例 + 单元测试 | ~400 | — |

### 2.2 关键难点

1. **统一 tree-sitter 解析**：多语言共用一套声明映射表；Go 无字段名时按子节点位置推断；声明名/函数子树 skipRanges 排除引用自指。
2. **接口动态分派**：interface 方法 → 所有实现类型的方法建立 `impl` 边（方法签名匹配）。
3. **FTS5 构建约束**：go-sqlite3 默认只启用 FTS3，FTS5 在 build tag `fts5` 下编译；**所有 build/test 必须 `-tags fts5`**，否则运行时报 `no such module: fts5`。
4. **Windows SQLite 文件锁**：一个 db 文件只能一个连接持有，Store 需 `Close()` 释放。
5. **BFS 去重方向**：入边 BFS 按 `From` 去重、出边按 `To` 去重，否则会漏上游/下游节点。
6. **DLL 按需加载**：仅配置 `embedding.model`（config.json，`EMBED_MODEL` 可覆盖）时加载 `llama_bridge.dll`；C 字符串用 `[]byte` 切片 + `runtime.KeepAlive`；错误写入调用方缓冲（避免 unsafe.Pointer vet 误报）。

---

## 3. 架构设计

```
internal/codegraph/
├── model.go        // 数据模型（Node/Edge/Index，无 gob 依赖）
├── parse_ts.go     // 所有语言（含 .go）统一 tree-sitter 通用提取器 + sourceExts 白名单
├── resolve.go      // 包聚合、Build（词法 resolveRef + 接口签名匹配 impl 边）
├── index.go        // 全量/增量构建、目录扫描（默认忽略 + .gitignore）、SQLite 落盘/加载、Vecs 回调
├── ignore.go       // 默认忽略清单（对齐官方）+ 根/嵌套 .gitignore 匹配、>1MB 限制
├── db.go           // SQLite 存储层（nodes/edges/files/vecs/meta + contentless FTS5）与 Search()
├── query.go        // MatchByID / CallersOf / BlastRadius / Trace（双向 BFS）
├── tslangs/        // 多语言 tree-sitter 解析（通用 AST 提取器 + 语言注册表）
│   ├── langs.go    // 语言注册表：15 种语言（Go/TS/TSX/JS/JSX/Py/Java/C/C++/C#/PHP/Ruby/Rust/Kotlin/Scala/Dart）
│   ├── extract.go  // 通用 AST 提取器：声明/方法/调用/签名/Doc（声明映射表驱动）
│   ├── kotlin/     // vendored C 语法（原版内核 sha 校验，ABI 14）+ cgo 包装
│   └── dart/       // vendored C 语法（原版内核 sha 校验，ABI 15）+ cgo 包装
└── *_test.go       // golden 样例 + 单元测试 + 真实项目冒烟 + 多语言 E2E

internal/embed/     // llama_bridge.dll 动态加载（syscall.NewLazyDLL）：模型加载/向量化/余弦
internal/semantic/  // 语义检索：FTS5 候选 → 余弦重排（可选）
third_party/llama-bridge/  // C 桥接层：bridge.c（封装复杂 C API 为 5 个导出函数）+ build.ps1 → llama_bridge.dll（源码入库，dll 产物忽略）
third_party/llama.cpp/  // git submodule 官方 llama.cpp（固定提交 ece98b87…；仅编译期静态库，经桥接层链接进 DLL；克隆需 --recursive）
models/             // GGUF embedding 模型（nomic-embed-text-v1.5.Q8_0.gguf，139MB；git 忽略，自备；路径由 config.json embedding.model 指定）
```

> **v1.3 统一架构 + embedding**：① 全部语言（含 Go）统一 tree-sitter 通用提取器；
> ② cgo 静态链接 llama.cpp 实现本地 embedding（当时方案）。
>
> **v2 重构（本次落地）**：
> ① **统一 tree-sitter**：删除 `types.go`（go/types 类型感知建边）与 `golang.org/x/tools`
> 依赖，全部 15 语言统一走 `tslangs` 词法提取 + `resolveRef` 词法解析；接口实现（impl 边）
> 按方法签名匹配保留。
> ② **存储迁移 SQLite**：`db.go` 新建 schema（nodes/edges/files/vecs/meta + contentless
> FTS5 虚拟表 nodes_fts），`Save(ix)`/`Load(ix)` 事务内全量读写（先 DELETE 再 INSERT）；
> 删除 gob 单文件（`index.gob` → `codegraph.db`）。**必须 `-tags fts5` 构建**。
> ③ **FTS5 全文检索**：`Search(query, limit)` 按 rank 返回候选 ID，替代内存词法打分；
> `semantic.Rerank` 对 FTS5 候选做可选语义重排（无语义时按候选顺序构造 Match）。
> ④ **llama.cpp DLL 动态链接**：C 桥接层 `bridge.c` 把复杂 llama C API 封装成 5 个导出
> 函数（`cg_embed_new/dim/error/encode/free`），编译为 `llama_bridge.dll`；Go 侧
> `syscall.NewLazyDLL` 按需加载——**仅配置 `embedding.model`（config.json，`EMBED_MODEL`
> 可覆盖）时加载**，否则完全不加载、回退 FTS5（原版 codegraph 的语义方案）。
> ⑦ **Vulkan 加速 + CPU 回退**：llama.cpp 静态库用 `-DGGML_VULKAN=ON` 构建（缺工具链
> 自动回退 CPU-only），`build.ps1` 检测到 `ggml-vulkan.a` 即一并链接；运行时 llama.cpp
> 自动探测 Vulkan 设备，无则回退 CPU；`build-gui.ps1` 每次自动构建 DLL 并复制到 build/。
> ⑤ **细粒度工具集**：删除单一 `codegraph_explore`，改为 6 个工具
> （search/node/callers/callees/trace/impact），Agent 按需取图，控制 token。
> ⑥ **深度调用链**：`CallersOf(id, depth)`/`Trace(id, depth)`/`BlastRadius(id, depth)`
> 基于 BFS（按前进方向节点去重）。

### 3.1 数据模型

```go
// model.go
type Kind int // Func / Method / Type / Interface / Struct / Var / Const

type Node struct {
    ID        int
    File      string // 相对项目根
    Line      int    // 声明行号（1 起）
    Kind      Kind
    Name      string
    Receiver  string // 方法接收者类型名（方法专用）
    Signature string // 函数签名摘要
    Doc       string // 注释首句（FTS5/Doc 检索用）
}

type EdgeKind int // Call / Ref / Impl / Define

type Edge struct {
    From, To int    // Node ID
    Kind     EdgeKind
    Site     string // 引用处 "file:line"
}

type Index struct {
    Nodes     []Node
    Edges     []Edge
    ByFile    map[string][]int  // file -> node ids
    ByName    map[string][]int  // name -> node ids
    FileFp    map[string]string // file -> 指纹（增量用）
    BuiltAt   time.Time
    Vecs      map[int][]float32 // 可选：node ID → embedding 向量（SQLite blob 持久化）
    VecDim    int               // 向量维度（0 = 未启用语义）
}
```

### 3.2 索引构建流程（index.go）

```
Reindex(root)：
  1. goFiles 扫描：默认忽略清单（与官方 DEFAULT_IGNORE_PATTERNS 完全一致，无额外硬编码）+ 根/嵌套
     .gitignore 过滤（目录被忽略即 SkipDir；vendored 目录如 third_party/ 由项目 .gitignore 治理），
     跳过 .git 与 .codegraph(-*)，>1MB 文件跳过，仅收录 sourceExts 白名单 30+ 扩展名，相对路径
  2. 对比 FileFp（sha256+mtime+size）→ 变更/新增/删除文件集合
  3. 变更集为空 → 直接返回（快路径）
  4. 重解析变更文件（未变更复用 s.graphs 缓存），删除失效缓存
  5. 全量 Build：统一 tree-sitter 提取 → 词法 resolveRef 建边 + 接口签名匹配 impl 边
  6. 可选向量化（VecBuilder 回调 → Vecs/VecDim）
  7. SQLite 落盘 codegraph.db（事务：DELETE + INSERT，含 FTS5 与 Vecs blob），跨进程互斥锁
```

### 3.3 查询（query.go + db.go）

```
Search(query, limit)：  nodes_fts MATCH ? ORDER BY rank LIMIT ?
CallersOf(id, depth)：  入边 BFS（按 From 去重），返回调用链
BlastRadius(id, depth)：出边 BFS（按 To 去重），影响面
Trace(id, depth)：      双向 BFS（e.From==cur → 下游、e.To==cur → 上游），调用路径
MatchByID(root, id)：   单符号完整上下文（Source + EdgesOf + BlastRadius）
语义重排：FTS5 候选 → 整串精确命中置顶 + 其余余弦 ≥0.25 降序；无语义时按候选顺序
```

### 3.4 参考实现（原版 codegraph 架构）

原版（github.com/colbymchenry/codegraph）为 TypeScript 实现：tree-sitter 多语言解析 + SQLite 存储（FTS5）+ 文件 watch 自动同步 + 图遍历工具（callers/callees/impact/trace，深度可调）。
本移植版 v2 收敛/扩展如下：

| 维度 | 原版 | 本移植版（v2） |
| :--- | :--- | :--- |
| 解析 | tree-sitter（20+ 语言） | tree-sitter（15 种主流，含 Go 统一走 tree-sitter） |
| 存储 | SQLite（FTS5） | SQLite（FTS5，contentless 虚拟表） |
| 全文检索 | SQLite FTS5 | SQLite FTS5（同款） |
| 同步 | 文件 watch | Agent 任务完成后触发重建 |
| 工具粒度 | 多工具按需取图 | 6 工具（search/node/callers/callees/trace/impact） |
| 语义 | 词法 + 服务端语义（平台版） | 词法 FTS5 + 可选本地 llama.cpp embedding（DLL 按需加载） |

第三方库约束：`github.com/mattn/go-sqlite3`（cgo，`-tags fts5` 启用 FTS5）+ `github.com/tree-sitter/go-tree-sitter`；embedding 经 `llama_bridge.dll`（syscall）按需加载，未配置模型时无运行时 DLL 依赖。

---

## 4. 实现阶段

### Phase 1：数据模型 + 单文件解析（model.go + parse.go）

- [x] 定义 Node/Edge/Index 结构
- [x] tree-sitter 提取声明/方法/调用/签名/Doc（tslangs 通用提取器）
- [x] 记录签名摘要与 Doc 首句
- **验证**：golden 小工程（2 文件）断言符号与边数量、行号、签名

### Phase 2：跨包解析 + 动态分派（resolve.go）

- [x] 按包聚合符号表（目录 = 包）
- [x] resolveRef 词法解析限定名（`pkg.Func`、`obj.Method`）
- [x] 接口方法 → 实现方法 `impl` 边（方法签名匹配）
- [x] 同名多候选放弃解析（精确优先，避免错误边）
- **验证**：跨包 + 接口/实现样例，断言 impl 边与跨文件 call 边

### Phase 3：查询引擎（query.go）

- [x] 词法打分排序（v1）→ **v2 改 FTS5**（nodes_fts MATCH + rank）
- [x] 命中符号间 BFS 调用路径；反向边影响面（含测试覆盖标记）
- [x] 源码按行号从磁盘读取（与 Read 工具一致），分组输出
- [x] v2：CallersOf / BlastRadius / Trace（双向 BFS，深度可调）
- **验证**：对本项目查询 `Agent.Run`、`Agent.SetSkills` 等，冒烟测试断言首条命中与源码

### Phase 4：落盘 + 增量（index.go）

- [x] 文件指纹（sha256+mtime+size）与变更集检测
- [x] 增量重建：复用未变更文件缓存图，仅重解析变更/新增/删除文件
- [x] **v2：SQLite 落盘**（DELETE + INSERT 事务，含 FTS5 与 Vecs blob）+ 跨进程互斥锁 + Store.Close() 释放连接
- **验证**：改一个文件后重建，断言仅该文件重解析（指纹）；查询反映新内容；删除文件同步移除

### Phase 5：工具集成 + 自动重建

- [x] v1：单一 `codegraph_explore` → **v2 拆为 6 工具**（search/node/callers/callees/trace/impact）注册进 Registry（bootstrap，FTS5 回调注入）
- [x] Agent Options 增加 `OnTaskDone func()` 钩子，bootstrap 接线为后台 `codegraph.Reindex(cwd)`
- [x] 索引缺失/未就绪时返回友好提示（"索引未初始化：首次构建进行中…"）
- [x] 启动异步预热索引；状态经 logx 推送 GUI 状态栏（`codegraph 索引就绪: N 符号 / M 关系`）；CLI `/codegraph` 命令手动重建；GUI 顶部"重建索引"按钮
- **验证**：完成任务后 codegraph.db 更新；工具可查询到刚改的符号

### Phase 6：验证与性能

- [x] 端到端：本项目全量索引耗时、增量重建耗时、查询延迟
- [x] 正确性：golden 测试全绿；冒烟测试在真实项目根上断言符号/关系/查询结果
- [ ] 极限：大目录（1 万+ 文件）全量耗时与内存（当前 55 文件规模远未触及瓶颈，留待后续）——**未做，后续**
- **验收**：查询任意符号返回源码+调用路径+影响面；改动后自动重建即查即新

**实测性能**（本项目根，AMD Ryzen 5 3500U）：
- v1.3 全量构建（含类型 Load，56 文件 / 470 符号 / 899 关系）：**~10.4s**；增量快路径 ~22ms
- v2（统一 tree-sitter，无类型 Load）：全量显著更快；增量快路径保持 ~22ms 级

### Phase 7：统一 tree-sitter + 本地 embedding（v1.3）

- [x] Go 语言统一走 tree-sitter：移除 go/ast 分流；tslangs 注册 Go（无字段名按子节点位置推断、声明名/函数子树 skipRanges 排除自指、goEmbedded/goCallRef 提取嵌入类型与包限定调用）
- [x] `internal/embed`：cgo 静态链接 third_party/llama.cpp 实现本地 embedding（v1.3 方案，v2 已被 DLL 替代）
- [x] `semantic.Service`：全量符号向量化，经 `Store.VecBuilder` 回调写入 `Index.Vecs/VecDim` 持久化；整串精确命中优先 + 余弦重排
- [x] bootstrap 接线：`EMBED_MODEL` 环境变量指定模型路径；未配置/加载失败仅 Warn，回退纯词法（不阻塞启动）
- **验证**：`TestE2E_RealRepo`（真实仓库 630 符号全量向量化 + 语义查询命中）+ 迷你索引语义测试全绿

### Phase 8：SQLite + FTS5 + DLL + 细粒度工具（v2）

- [x] 删除 `types.go`（go/types 类型感知）与 `golang.org/x/tools` 依赖；全部语言统一 tree-sitter 词法提取 + resolveRef
- [x] `db.go`：SQLite schema（nodes/edges/files/vecs/meta + contentless FTS5）+ Save/Load/Search；删除 gob
- [x] `embed` 改为 syscall 动态加载 `llama_bridge.dll`（C 桥接层 bridge.c，5 个导出函数；build.ps1 编译）；仅配置 `embedding.model`（config.json，`EMBED_MODEL` 可覆盖）时加载
- [x] `semantic.Rerank`：FTS5 候选语义重排；无语义回退按候选顺序构造 Match
- [x] 6 细粒度工具（search/node/callers/callees/trace/impact）替换 `codegraph_explore`；深度调用链 BFS（按前进方向去重）
- [x] 目录扫描对齐官方：默认忽略清单 + 根/嵌套 `.gitignore`（`ignore.go`，sabhiram/go-gitignore）+ >1MB 文件跳过；`TestDumpGoFiles` 与官方 `compare-scan.mjs`（node-ignore 语义）对 32 文件/20+ 场景比对**完全一致**（默认清单与官方一致，无额外硬编码目录；vendored 目录由项目 .gitignore 治理）
- [x] build-gui.ps1 自动构建 `llama_bridge.dll` 并复制到 build/（缺静态库自动 cmake，优先 `-DGGML_VULKAN=ON`，失败回退 CPU-only）；README / PLAN 同步 `-tags fts5`、Vulkan 加速与 CPU 回退说明
- **验证**：`go vet -tags fts5 ./internal/...` 通过；`go build -tags fts5 ./cmd/...` 通过；`go test -tags fts5 ./...` 全绿（codegraph 63.5s / semantic 100.5s 含真实仓库向量化，TestEmbedBasic 验证 DLL 加载同文 sim=1.0/异题 0.57）

---

## 5. 关键设计决策

| 决策点 | v2 默认 | 备选 |
| :--- | :--- | :--- |
| 语言范围 | Go + 主流 15 种（统一 tree-sitter） | 全 36 种（冷门语言 cgo 包装，后续） |
| 存储 | SQLite（codegraph.db，FTS5） | gob 单文件（v1，已废弃） |
| 全文检索 | SQLite FTS5（MATCH + rank） | 内存词法打分（v1，已废弃） |
| 语义检索 | 词法 FTS5 + 可选 llama.cpp embedding（**DLL 按需加载**） | 服务端 embedding API、cgo 静态链接（v1.3，已废弃） |
| 方法调用解析 | 同名多候选时放弃（精确优先，避免错误边） | 静态类型推断（scope 分析，v2+） |
| 接口实现判定 | 方法名 + 参数/返回值个数匹配 | go/types 完整类型检查（已移除） |
| 工具粒度 | 6 工具按需取图（search/node/callers/callees/trace/impact） | 单一 explore 全量返回（v1，token 浪费，已废弃） |
| 项目根 | 进程工作目录（与文件工具一致） | 配置项显式指定 |
| 自动重建 | Run 结束后台增量 | 文件变更实时重建（watch） |

---

## 6. 验收标准（Definition of Done）

- [x] 6 个 codegraph 工具注册并可被模型调用
- [x] 查询本项目任意符号：返回源码 + 调用路径 + 影响面 + 测试覆盖提示
- [x] FTS5 全文检索可用（`-tags fts5` 构建）；语义可用时对候选重排
- [x] 深度调用链：callers（入边递归）/ trace（双向 BFS）/ impact（出边 BFS），深度可调
- [x] 每次任务完成自动重建；改代码后立刻可查到新符号
- [x] 增量重建显著快于全量（快路径 ~22ms）；后台重建不阻塞用户下一次输入
- [x] 新增依赖：`github.com/mattn/go-sqlite3`（cgo，`-tags fts5`）+ `tree-sitter/go-tree-sitter` + vendored Kotlin/Dart C 语法；embedding 经 `third_party/llama-bridge/llama_bridge.dll`（syscall 动态加载，未配置模型时零 DLL 依赖）；`go test -tags fts5 ./...` 全绿
- [x] 多语言覆盖主流 15 种（统一 tree-sitter 提取器测试 + 多语言混合项目 E2E 全绿）
- [x] 语义检索：`embedding.model`（config.json，`EMBED_MODEL` 可覆盖）指向 GGUF 模型时启用本地 embedding（DLL 动态加载，Vulkan 加速/CPU 回退；FTS5 候选重排）；未配置/模型缺失自动回退 FTS5 全文检索（不加载 DLL，功能可用不阻塞）

### 已知限制（v2）

1. **词法级解析**：全部语言为词法级 tree-sitter（声明/调用/签名/Doc），无类型感知；跨包引用按模块语义词法解析，同名多候选放弃（精确优先）。删除 go/types 后此限制适用于全部语言（含 Go），换取多语言行为一致与更低的维护成本。
2. **目录扫描**：默认忽略清单（与官方 `DEFAULT_IGNORE_PATTERNS` **完全一致**，约 90 个依赖/构建/缓存目录 + `*.egg-info/`/`cmake-build-*/`/`bazel-*/`/`**/res/{values,…}*/`）+ 根/嵌套 `.gitignore`（根与默认合并，支持 `!` 否定默认；嵌套相对其目录求值）+ >1MB 文件跳过；`.git` 与 `.codegraph(-*)` 始终跳过；目录被忽略即不进入（父目录规则天然成立）。vendored 源码目录（如 `third_party/`）不硬编码忽略，由项目 `.gitignore` 治理。已与官方 `scanDirectoryWalk` 对 32 文件/20+ 场景比对，默认清单对齐后输出**完全一致**；差异：`?` 按字面量、不跟随 symlink、不走 `git ls-files`、无 codegraph.json include/exclude。
3. **FTS5 构建约束**：必须 `-tags fts5`（go-sqlite3 默认仅 FTS3），否则 `no such module: fts5`。
4. **Windows SQLite 文件锁**：一个 db 文件仅支持单连接，测试/多实例需 `Close()` 释放。
5. **首个全量构建/向量化较慢**：启用 embedding 后首次索引需向量化全量符号（本项目 630 符号约 60s），后台执行不阻塞交互。
6. **跨包嵌入**：结构体嵌入仅支持同包类型（`pkg.Type` 嵌入不参与方法集合并）。
7. **stdlib/外部依赖**：仅索引本 module 代码，`fmt.Println` 等 stdlib 调用不建边（不产生外部符号节点）。
8. **语义检索**：embedding 模型为英文（nomic-embed-text-v1.5），中文 Doc/注释占比高的符号语义区分度有限；语义召回依赖向量质量；DLL 由 build-gui.ps1 自动构建（Vulkan 加速/CPU 回退，构建失败仅告警），模型 139MB 自备。
