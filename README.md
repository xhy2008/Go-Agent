# go-agent

极简、原生、单进程的 Coding Agent。纯 Go 实现，GUI 基于 **Qt6（miqt Go 绑定，CGO 直调）**，无浏览器内核。

- **对话式编程辅助**：支持工具调用（文件读写、命令执行、内容搜索、联网搜索）。
- **原生 GUI**：三栏布局（会话列表 / 聊天消息 / 修改文件），消息气泡、命令输出折叠块、流式 Markdown 渲染、右键复制命令。
- **会话持久化**：自动保存历史，启动时恢复上次会话。
- **长期记忆**：本地向量/关键词检索（`memory/agent.db`）。
- **双入口**：`agent`（CLI 交互）与 `agent-gui`（桌面 GUI）。

## 目录结构

```
├── cmd/
│   ├── agent/          # CLI 入口
│   ├── agent-gui/      # Qt6 GUI 入口（含 app.manifest / rsrc.syso 资源）
│   └── guismoke/       # GUI 冒烟测试（模拟流式输出+折叠点击）
├── internal/
│   ├── agent/          # Agent ReAct 循环
│   ├── bootstrap/      # 应用装配（配置、LLM、搜索、记忆、会话、工具注册）
│   ├── config/         # 配置加载（默认值 < config.json < 环境变量）
│   ├── llm/            # OpenAI 兼容 LLM 客户端（流式、token 统计）
│   ├── logx/           # 结构化日志（可回传 GUI 状态栏）
│   ├── memory/         # 长期记忆持久化
│   ├── search/         # Brave / SearXNG / DuckDuckGo Lite
│   ├── session/        # 会话历史读写
│   ├── skill/          # Skill 加载
│   └── tools/          # 工具集（文件、命令、搜索、WebSearch 等）
├── build/              # 部署目录：exe + Qt DLL/插件 + 运行数据（git 忽略）
├── build-gui.ps1       # 构建 agent-gui 并部署 Qt 运行库到 build/
├── config.example.json # 配置模板（不含密钥）
└── qt.conf             # Qt 平台配置（DPI 感知级别）
```

> `build/` 内的 `config.json`、`sessions/`、`memory/` 是**运行数据**，包含密钥与历史记录，不入库。仓库只提交源码与文档。

## 环境要求（Windows）

- **Go 1.21+**（含 CGO 工具链）
- **MSYS2 ucrt64** + **Qt6**（DLL 位于 `C:\msys64\ucrt64\bin`，插件位于 `C:\msys64\ucrt64\share\qt6\plugins`）

## 构建与运行

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
# CLI 版本
go build -o build/agent.exe ./cmd/agent
.\build\agent.exe

# GUI 冒烟测试（自动执行后退出）
go build -o build/guismoke.exe ./cmd/guismoke
.\build\guismoke.exe
```

> 首次运行会在 exe 同目录自动生成默认 `config.json`。所有数据文件（`config.json`、`sessions/`、`memory/`）均与 exe 同目录。

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
  }
}
```

支持环境变量覆盖：`LLM_API_KEY`、`LLM_BASE_URL`、`LLM_MODEL`、`DEEPSEEK_API_KEY`、`BRAVE_API_KEY`、`SEARXNG_URL`。

> 注意：`build/config.json` 含密钥，已通过 `.gitignore` 排除；向仓库添加配置时请使用 `config.example.json` 并留空密钥。
