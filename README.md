# go-agent

极简、原生、单进程的 Coding Agent。纯 Go 实现，GUI 基于 **Qt6（miqt Go 绑定，CGO 直调）**，无浏览器内核。

## 核心卖点：本地代码知识图谱 + 语义检索

Agent 内置本地代码知识图谱（**codegraph**），一次构建、随改随查，让 Agent 不再靠 grep/glob/Read 反复扫描代码，一次调用即可拿到完整上下文：

- **15 种主流语言统一 tree-sitter 解析**（Go/TS/TSX/JS/JSX/Python/Java/C/C++/C#/PHP/Ruby/Rust/Kotlin/Scala/Dart），所有语言（含 Go）共用一套通用 AST 提取器，符号、调用、引用、接口实现关系全量建图；不再有 go/ast、go/types 的单独解析路径，仅按 tree-sitter 各语言语法节点的差异做少量适配（如 Go 语法无字段名、按子节点位置推断）
- **SQLite + FTS5 全文索引**：索引持久化到 `<项目根>/.codegraph/codegraph.db`（nodes/edges/files 表 + contentless FTS5 虚拟表），全文检索由 SQLite FTS5 提供（`MATCH` + `rank` 排序），与原版 codegraph 同款方案
- **深度调用链检索**：`callers`（入边递归，深度可调）、`callees`（出边递归）、`trace`（双向 BFS 调用路径）、`impact`（影响面 blast radius），对齐原版 codegraph 的图遍历能力
- **细粒度工具集**：6 个独立 MCP 工具（`search`/`node`/`callers`/`callees`/`trace`/`impact`），Agent 按需取图，不再一次性全量返回浪费 token
- **本地 embedding 语义检索（独家，可选）**：DLL 动态链接 llama.cpp（`llama_bridge.dll`，带 **Vulkan 加速**，无 GPU 时自动回退 CPU），仅在配置了向量化模型（`config.json` 的 `embedding.model`，`EMBED_MODEL` 环境变量可覆盖）时才加载 DLL，全量符号向量化后支持**自然语言模糊查询**——"含义相同但名字不同"的符号也能命中；未配置模型时**完全不加载 DLL**，回退到原版 codegraph 的 FTS5 全文检索方案，零额外依赖。官方版 codegraph 明确不依赖 embedding，无法回答此类模糊语义问题，这是本移植版的增量能力
- **自动增量重建**：每轮任务结束后台自动同步（指纹比对，仅重解析变更文件）；`/codegraph` 命令或 GUI 顶部"重建索引"按钮可手动触发
- **100% 本地**：索引（SQLite）、符号向量（随索引持久化）、模型推理全部在本机完成，无外部服务、无 API Key

其他特性：

- **对话式编程辅助**：支持工具调用（文件读写、命令执行、内容搜索、联网搜索、代码图查询）。
- **原生 GUI**：三栏布局（会话列表 / 聊天消息 / 修改文件），消息气泡、命令输出折叠块、流式 Markdown 渲染、右键复制命令。
- **会话持久化**：自动保存历史，启动时恢复上次会话。
- **长期记忆**：本地向量/关键词检索（`memory/agent.db`）。
- **双入口**：`agent`（CLI 交互）与 `agent-gui`（桌面 GUI）。

## 代码知识图谱（CodeGraph）

### 工作方式

- **解析**：所有语言（含 Go）统一走 tree-sitter 通用 AST 提取器（一套 `decls` 声明映射表驱动，无语言特判），提取声明/方法/调用/签名/Doc；接口实现关系（`impl` 边）按方法签名匹配建立。
- **存储**：索引写入 `<项目根>/.codegraph/codegraph.db`（SQLite，单连接 + 跨进程互斥锁），含 `nodes/edges/files` 表与 **contentless FTS5** 虚拟表 `nodes_fts`（全文检索）；符号向量（Vecs）以二进制 blob 随索引持久化。
- **同步**：每轮任务结束后台增量重建（mtime + sha256 指纹，未变更文件复用缓存图）；`/codegraph` 或 GUI 按钮手动全量重建。
- **全文检索**：`internal/codegraph` 的 `Search(query, limit)` 直接执行 `SELECT ... FROM nodes_fts WHERE nodes_fts MATCH ? ORDER BY rank`（FTS5 相关度排序），替代旧的内存词法打分。
- **语义检索（可选）**：`internal/embed` 通过 syscall **动态加载 `llama_bridge.dll`**（C 桥接层，封装 llama.cpp 的模型加载/向量化 API），仅为全部符号生成向量（文本 = 接收者.符号名 + 签名 + Doc）；查询时先 FTS5 召回候选，再对候选做语义重排（整串精确命中置顶 + 其余余弦 ≥ 0.25 降序）。**未配置模型路径（`config.json` 的 `embedding.model`，`EMBED_MODEL` 可覆盖）时完全不加载 DLL**，直接使用 FTS5 全文检索结果。

### 目录扫描逻辑

`Reindex(root)` 构建索引时按以下规则收集源文件（`goFiles`，对齐官方 codegraph 的 `scanDirectoryWalk`）：

1. **递归遍历**：从项目根递归收集；`.git` 与索引数据目录（`.codegraph` 及 `.codegraph-*`）始终跳过；不可读目录/文件跳过不中断。
2. **默认忽略清单**（无 `.gitignore` 也生效，对齐官方 `DEFAULT_IGNORE_PATTERNS`）：依赖/构建/缓存/工具输出目录约 90 个——`node_modules/`、`bower_components/`、`.yarn/`、`dist/`、`build/`、`out/`、`.next/`、`.nuxt/`、`coverage/`、`__pycache__/`、`.venv/`、`venv/`、`.mypy_cache/`、`target/`、`.gradle/`、`obj/`、`vendor/`、`Pods/`、`.build/`、`.dart_tool/`、`.pub-cache/`、`.cxx/`、`.cache/` 等，另含 `*.egg-info/`、`cmake-build-*/`、`bazel-*/`、`**/res/{values,layout,drawable,…}*/`（Android 资源目录）。与官方清单**完全一致**（无额外硬编码目录）。
3. **`.gitignore` 解析**：根目录 `.gitignore` 与默认清单合并进同一个匹配器（因此根 `.gitignore` 的否定规则如 `!vendor/` 可覆盖默认忽略——官方文档的 opt-in 方式）；子目录的 `.gitignore` 各自编译、相对其声明目录求值（同 git 嵌套语义）。目录被忽略即整棵子树不进入，因此"父目录被忽略则子文件无法用 `!` 重纳入"的 git 规则天然成立。项目内的 vendored 目录（如 `third_party/`）由项目 `.gitignore` 治理，不在代码中硬编码。
4. **文件筛选**：仅收录 `sourceExts` 白名单内的扩展名（15 种语言共 30+ 扩展名，见下方"支持语言"），大小写不敏感；**>1MB 的文件跳过**（生成的 bundle/压缩产物，对齐官方 `MAX_FILE_SIZE`）。
5. **相对路径**：收录文件以项目根为基准的相对路径（正斜杠分隔）存入索引。

> 行为比对：对 32 文件/20+ 忽略场景的目录树，移植版与官方 `scanDirectoryWalk`（node-ignore 语义）输出**完全一致**（默认清单对齐后无差异）。已知细微差异见"已知限制"。

### 使用方式

1. **Agent 自动使用**：注册 6 个细粒度工具——`codegraph_search`（关键词/FTS5 检索，紧凑列表）、`codegraph_node`（单符号完整上下文）、`codegraph_callers`（入边递归，深度 1-3）、`codegraph_callees`（出边递归）、`codegraph_trace`（双向 BFS 调用链，默认深 3）、`codegraph_impact`（影响面，默认深 2）。模型按需调用，只取自己需要的粒度。
2. **CLI 手动重建**：对话中输入 `/codegraph`，同步重建并输出 `N 符号 / M 关系（耗时 xx）`。
3. **GUI 手动重建**：点击顶部状态栏右侧"重建索引"按钮（后台执行，不阻塞界面）。

### 启用语义检索

模型路径在 **`config.json` 的 `embedding.model`** 中指定（不再自动探测 `models/` 目录）；环境变量 `EMBED_MODEL` 可覆盖配置文件。配置了模型路径时加载 `llama_bridge.dll` 全量向量化；未配置（或 `"off"`/`"0"`）时回退 FTS5 全文检索，**完全不加载 DLL**。

```json
// config.json（与 exe 同目录）
{
  "embedding": {
    "model": "D:\\models\\nomic-embed-text-v1.5.Q8_0.gguf"
  }
}
```

```powershell
# 环境变量覆盖配置文件（可选；"off" 显式关闭）
$env:EMBED_MODEL = "D:\models\nomic-embed-text-v1.5.Q8_0.gguf"
```

模型需自备（nomic-embed-text-v1.5 的 GGUF 量化版，Q8_0 约 139MB，768 维，从 HuggingFace 下载即可）。启用后启动日志会出现 `语义检索已启用`。

> 需要 `third_party/llama-bridge/llama_bridge.dll`（`build-gui.ps1` 会自动构建并复制到 `build/`，见下方"构建 llama_bridge.dll"）。DLL 缺失时语义检索自动降级为 FTS5，不影响启动与全文检索。

### 支持语言

Go、TypeScript（含 `.mts/.cts`）、TSX、JavaScript（含 `.mjs/.cjs/.jsx`）、Python（含 `.pyw`）、Java、C（含 `.h`）、C++（`.cc/.cxx/.hpp/.hxx/.hh`）、C#、PHP（`.module/.inc`）、Ruby（`.rake`）、Rust、Kotlin（`.kts`）、Scala（`.sc`）、Dart。

### 已知限制

- 全部语言为词法级 tree-sitter 解析（声明/调用/签名/Doc），无类型感知；跨包引用按模块语义解析（`resolveRef` 词法解析），同名多候选时放弃解析（精确优先，避免错误边）。
- 跨文件/跨语言桥接（如 iOS Swift+JS 混合）与框架路由识别暂未支持。
- **目录扫描与原版的差异**：① `?` 单字符通配符按字面量处理（git/node-ignore 支持，移植版所用 gitignore 库的限制，实际 .gitignore 中罕见）；② 不跟随符号链接（原版跟随 in-root symlink 并去重）；③ git 仓库中不调用 `git ls-files`/`.git/info/exclude`/全局 excludesfile（原版在 git 仓库走 git 枚举），均改用直接读 `.gitignore`（根+嵌套），与官方非 git 项目路径一致；④ 不支持 `codegraph.json` 的 include/exclude/includeIgnored 配置（官方有，移植版无配置系统）。
- 英文 embedding 模型对中文 Doc 占比高的符号区分度有限；启用语义后首次向量化较慢（约 630 符号 60s，后台执行不阻塞交互）。
- 语义查询依赖 `llama_bridge.dll`（syscall 动态加载，见下方构建说明）；未配置模型时不加载 DLL，仅用 FTS5 全文检索。
- 全量构建需在 `go build/test` 时附加 **`-tags fts5`**（go-sqlite3 默认只启用 FTS3，FTS5 在 fts5 build tag 下编译）——无此 tag 会报 `no such module: fts5`。

## 目录结构

```
├── cmd/
│   ├── agent/          # CLI 入口（含 /codegraph 命令）
│   ├── agent-gui/      # Qt6 GUI 入口（顶部"重建索引"按钮）
│   └── guismoke/       # GUI 冒烟测试（模拟流式输出+折叠点击）
├── internal/
│   ├── agent/          # Agent ReAct 循环
│   ├── bootstrap/      # 应用装配（配置、LLM、搜索、记忆、会话、工具注册、语义服务）
│   ├── codegraph/      # 代码知识图谱：模型/SQLite+FTS5 持久化、tree-sitter 解析、增量索引、FTS5 查询、深度调用链
│   │   ├── db.go       # SQLite 存储层（nodes/edges/files/vecs + contentless FTS5）与 Search()
│   │   ├── query.go    # MatchByID / CallersOf / BlastRadius / Trace（双向 BFS 调用链）
│   │   └── tslangs/    # 多语言 tree-sitter 注册表 + 通用 AST 提取器（含 vendored Kotlin/Dart 内核）
│   ├── config/         # 配置加载（默认值 < config.json < 环境变量）
│   ├── embed/          # llama.cpp DLL 动态加载：模型加载 / 向量化 / 余弦相似度（syscall.NewLazyDLL）
│   ├── llm/            # OpenAI 兼容 LLM 客户端（流式、token 统计）
│   ├── logx/           # 结构化日志（可回传 GUI 状态栏）
│   ├── memory/         # 长期记忆持久化
│   ├── search/         # Brave / SearXNG / DuckDuckGo Lite
│   ├── semantic/       # 语义检索：FTS5 候选 → llama.cpp 向量重排（可选）
│   ├── session/        # 会话历史读写
│   ├── skill/          # Skill 加载
│   └── tools/          # 工具集（文件、命令、搜索、codegraph 6 工具等）
├── third_party/
│   ├── llama-bridge/   # C 桥接层：bridge.c + build.ps1 → llama_bridge.dll（syscall 动态加载；源码入库，dll 产物忽略）
│   ├── llama.cpp/      # git submodule（官方 llama.cpp，固定提交，仅编译期静态库，经桥接层链接进 DLL；克隆需 --recursive）
│   └── codegraph-upstream/  # 原版 codegraph 克隆（仅比对参考，git 忽略）
├── models/             # GGUF embedding 模型（git 忽略，自备）
├── build/              # 部署目录：exe + Qt DLL/插件 + 运行数据（git 忽略）
├── build-gui.ps1       # 构建 agent-gui 并部署 Qt 运行库到 build/
├── config.example.json # 配置模板（不含密钥）
└── qt.conf             # Qt 平台配置（DPI 感知级别）
```

> `build/` 内的 `config.json`、`sessions/`、`memory/` 是**运行数据**，包含密钥与历史记录，不入库。仓库只提交源码与文档。

## 环境要求（Windows）

- **Go 1.21+**（含 CGO 工具链——go-sqlite3 为 cgo 实现）
- **MSYS2 ucrt64** + **Qt6**（DLL 位于 `C:\msys64\ucrt64\bin`，插件位于 `C:\msys64\ucrt64\share\qt6\plugins`）
- 构建语义检索 DLL 需 **CMake + Ninja + MSYS2 gcc**（`build-gui.ps1` 自动完成）；Vulkan 加速可选装 MSYS2 包 `mingw-w64-ucrt-x86_64-vulkan-loader` + `mingw-w64-ucrt-x86_64-spirv-headers`，运行依赖 `vulkan-1.dll`（Windows 10 起系统自带），无 GPU/驱动时自动回退 CPU

## 构建与运行

> **重要**：构建/测试必须附加 `-tags fts5`（go-sqlite3 的 FTS5 模块在 fts5 build tag 下启用），否则运行时报 `no such module: fts5`。

> **首次克隆**：`third_party/llama.cpp` 是 git submodule（语义检索 DLL 编译依赖），请用 `git clone --recursive` 克隆，或克隆后执行 `git submodule update --init`。

```powershell
# 构建 agent-gui 并部署 Qt 运行库到 build/（build-gui.ps1 会设置 MSYS2/Go 环境变量）
powershell -File build-gui.ps1

# 构建后直接启动 GUI
powershell -File build-gui.ps1 -Run

# 手动启动
.\build\agent-gui.exe
```

其他入口：

```powershell
# CLI 版本（注意 -tags fts5）
$env:CGO_ENABLED = "1"
go build -tags fts5 -o build/agent.exe ./cmd/agent
.\build\agent.exe

# GUI 冒烟测试（自动执行后退出）
go build -tags fts5 -o build/guismoke.exe ./cmd/guismoke
.\build\guismoke.exe
```

> 首次运行会在 exe 同目录自动生成默认 `config.json`。所有数据文件（`config.json`、`sessions/`、`memory/`）均与 exe 同目录。

### 构建 llama_bridge.dll（启用语义检索必需，自动）

`internal/embed` 通过 `syscall.NewLazyDLL` 按需加载 `llama_bridge.dll`（加载优先级：exe 同目录 → 当前工作目录 → 仓库 `third_party/llama-bridge/`）。**未配置模型路径时不加载该 DLL**，程序零额外运行时依赖。

`build-gui.ps1` 会**自动完成整条构建链路**（每次构建 GUI 时执行，无需手动操作）：

1. 若 `third_party/llama.cpp/build` 尚无静态库，自动执行 CMake 配置与编译：**优先 `-DGGML_VULKAN=ON`**（启用 Vulkan 加速后端）；若本机缺 Vulkan 工具链导致配置失败，自动回退到 CPU-only 重新配置。
2. 调用 `third_party/llama-bridge/build.ps1`，用 MSYS2 gcc 把 `bridge.c` 与 llama.cpp 静态库链接为 `llama_bridge.dll`（存在 Vulkan 静态库 `ggml-vulkan.a` 时自动一并链接）。
3. 将 DLL 复制到 `build/`（与 exe 同目录，程序优先从此加载）。DLL 构建失败仅告警，不影响主程序构建与运行。

**Vulkan 加速与 CPU 回退**：启用 Vulkan 后端的 DLL 在运行时由 llama.cpp 的 `ggml_backend_load_all()` 自动探测——检测到 Vulkan 设备则走 GPU 计算（启动日志可见 `Vulkan0 ...`），无驱动/无设备时自动回退 CPU，全程无需任何配置。运行依赖 `vulkan-1.dll`（Windows 10 起系统自带）。

手动方式（与自动步骤等价，升级 llama.cpp 后可用）：

```powershell
# 1. 构建 llama.cpp 静态库（优先 Vulkan；失败则去掉 -DGGML_VULKAN=ON 重试）
cd third_party/llama.cpp
cmake -B build -G Ninja -DCMAKE_BUILD_TYPE=Release `
  -DGGML_VULKAN=ON `
  -DLLAMA_BUILD_EXAMPLES=OFF -DLLAMA_BUILD_TESTS=OFF -DLLAMA_CURL=OFF
cmake --build build --target llama

# 2. 编译 C 桥接层 → llama_bridge.dll
cd third_party/llama-bridge
powershell -File build.ps1
```

之后用 MSYS2 gcc 编译 Go 代码（go-sqlite3 的 cgo 需要 gcc 在 PATH 中）：

```powershell
$env:PATH = "C:\msys64\ucrt64\bin;" + $env:PATH
$env:CGO_ENABLED = "1"
go build -tags fts5 -o build/agent.exe ./cmd/agent
```

## 配置

配置文件 `config.json`（与 exe 同目录，参考根目录 `config.example.json`）：

```json
{
  "llm": {
    "base_url": "https://api.deepseek.com",
    "api_key": "你的 API Key",
    "model": "deepseek-chat",
    "context_window": 1000000
  },
  "search": {
    "backend": "",              // brave / searxng，留空则搜索不可用
    "brave_api_key": "",
    "searxng_url": ""
  },
  "embedding": {
    "model": ""                 // GGUF embedding 模型绝对路径；留空/"off"/"0" 关闭语义检索
  }
}
```

支持环境变量覆盖：`LLM_API_KEY`、`LLM_BASE_URL`、`LLM_MODEL`、`DEEPSEEK_API_KEY`、`BRAVE_API_KEY`、`SEARXNG_URL`；语义检索模型路径另有 `EMBED_MODEL`（覆盖 `embedding.model`，见上文"启用语义检索"）。

> 注意：`build/config.json` 含密钥，已通过 `.gitignore` 排除；向仓库添加配置时请使用 `config.example.json` 并留空密钥。
