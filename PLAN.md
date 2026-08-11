# 极简原生 Coding Agent 完整设计方案

## 1. 总览

本项目旨在构建一款**轻量级、无运行时依赖、原生GUI**的编程助手Agent。主要特点：

- **纯Go实现**，编译后单二进制文件，跨平台（Windows/macOS/Linux）。
- **原生GUI**（Fyne），无需浏览器，响应迅速。
- **核心能力**：
  - 对话式编程辅助，支持工具调用（文件读写、命令执行、内容搜索、联网搜索）。
  - Skill 加载（动态注入上下文）。
  - 项目记忆持久化（短期/长期）。
  - 实时命令输出、打断操作、上下文用量显示、文件修改跟踪。
- **联网搜索**：支持 **Brave Search API** 或本地 **SearXNG** 实例（用户可配置），并内置 DuckDuckGo Lite 作为备用零配置方案。
- **安全策略**：命令执行黑名单，危险操作需确认。

------

## 2. 技术栈选型

| 组件             | 选择                                  | 理由                                                |
| :--------------- | :------------------------------------ | :-------------------------------------------------- |
| 语言             | Go 1.21+                              | 编译无依赖，协程轻量，标准库强大，调试方便（Delve） |
| GUI              | Fyne v2                               | 原生渲染，跨平台，单二进制，无额外依赖，API简洁     |
| LLM调用          | 标准HTTP + SSE                        | 支持任何OpenAI兼容接口（Claude/GPT/本地），流式响应 |
| 嵌入向量（记忆） | 可选：`go-ggllm`或`ollama` API        | 轻量级RAG，使用`nomic-embed-text`或TF‑IDF降级       |
| 搜索引擎         | Brave API + SearXNG API + 备用DDG爬虫 | 用户可选，备用零配置                                |
| 结构化日志       | `slog`                                | 内置，可输出到GUI显示区域                           |
| 配置管理         | JSON/YAML文件                         | 存储API密钥、黑名单、用户偏好                       |

------

## 3. 整体架构

text

```
+-------------------+
|   Fyne GUI 主窗口   |
| - 对话历史区        |
| - 实时输出区 (命令/思考)|
| - 侧边栏 (文件修改列表)|
| - 状态栏 (token用量) |
| - 输入框 + 发送/停止按钮 |
+--------+----------+
         | 事件/通道
+--------v----------+
|    Controller      | (管理协程、context、channel)
| - 主Loop协程       |
| - 命令执行子协程    |
| - LLM流式接收      |
+--------+----------+
         |
+--------v----------+
|   Agent Core       |
| - ReAct循环        |
| - 工具注册与调用   |
| - Skill加载器      |
| - 记忆管理器       |
+--------+----------+
```



- **分离UI与业务**：UI事件通过`channel`发送指令，Agent运行在独立协程。
- **所有阻塞操作（LLM、命令、网络）均支持`context`取消**，实现打断。

------

## 4. 核心数据结构

### 4.1 工具接口

go

```
type Tool interface {
    Name() string
    Description() string
    ArgsSchema() map[string]interface{} // JSON Schema
    Execute(ctx context.Context, args map[string]interface{}) (string, error)
}
```



### 4.2 消息结构（用于LLM）

go

```
type Message struct {
    Role    string `json:"role"`    // system/user/assistant/tool
    Content string `json:"content,omitempty"`
    ToolCalls []ToolCall `json:"tool_calls,omitempty"`
    ToolCallID string `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
    ID   string `json:"id"`
    Type string `json:"type"` // "function"
    Function struct {
        Name      string `json:"name"`
        Arguments string `json:"arguments"` // JSON string
    } `json:"function"`
}
```



### 4.3 工具定义（给LLM注册）

go

```
type ToolDef struct {
    Type     string                 `json:"type"` // "function"
    Function FunctionDef            `json:"function"`
}

type FunctionDef struct {
    Name        string                 `json:"name"`
    Description string                 `json:"description"`
    Parameters  map[string]interface{} `json:"parameters"`
}
```



### 4.4 Skill 格式约定

- Skill 存放在 `<项目根>/.agent/skills/` 目录下，每个子目录或单个`.md`文件。

- 文件格式：Markdown，开头含**YAML Frontmatter**：

  yaml

  ```
  ---
  name: "python-expert"
  description: "适用于Python项目的开发助手，擅长虚拟环境和包管理"
  triggers: ["python", "pip", "venv"]
  version: "1.0"
  ---
  # 角色指令
  你是一个Python专家...
  ```

  

- 加载时：解析Frontmatter，将正文作为`system`消息的一部分注入（若触发词匹配用户问题）。

------

## 5. 工具实现细节

### 5.1 文件读写（支持行范围）

go

```
// 读取文件指定行范围（包含首尾）
func ReadFileRange(path string, startLine, endLine int) (string, error) {
    // 使用 bufio.Scanner 逐行读取，返回 lines[startLine-1 : endLine]
    // 若 endLine<=0 则读到末尾
}

// 写入文件（覆盖/追加）
func WriteFile(path, content string, append bool) error
```



### 5.2 文件搜索

- **按文件名搜索**：递归遍历目录，支持通配符（`filepath.Match`）。
- **在文件内容中搜索**：遍历文件，逐行匹配正则或关键字，返回 `file:line:content` 列表。

go

```
type SearchResult struct {
    File    string
    LineNum int
    Content string
}
func SearchInFiles(root, pattern string, contentRegex string) ([]SearchResult, error)
```



- 性能优化：可跳过二进制文件（`http.DetectContentType`），大型文件只读前1MB。

### 5.3 命令执行（黑名单）

go

```
var blacklist = []string{
    "rm -rf /", "mkfs", "dd if=", "shutdown", "reboot",
    ":(){ :|:& };:", // fork炸弹
}

func ExecuteCmd(ctx context.Context, cmdStr string) (output string, err error) {
    // 1. 安全检查：包含黑名单子串则直接拒绝
    // 2. 解析命令和参数（避免shell注入，使用 exec.Command 直接传递参数）
    // 3. 设置 context.WithCancel 支持打断
    // 4. 实时输出通过 channel 发送给UI（逐行）
    // 5. 限制最大输出行数（防溢出）
}
```



### 5.4 联网搜索（三种后端）

统一抽象接口：

go

```
type SearchEngine interface {
    Search(ctx context.Context, query string) ([]SearchResult, error)
}
```



- **BraveSearch**：使用官方API，需`API_KEY`。
- **SearXNG**：需用户指定实例URL（如`http://localhost:8080`），调用`/search?q=...&format=json`。
- **DuckDuckGo Lite**：内置备用，爬取`lite.duckduckgo.com`。

用户可通过GUI设置选择默认引擎，并提供相应参数（密钥、URL）。

### 5.5 记忆持久化

- **短期记忆**：存储在`~/.agent/memory/short.json`，保存最近N轮对话摘要（由LLM生成）。
- **长期记忆**：使用**向量检索**（轻量）。可选：
  - 调用本地Ollama embedding API（`nomic-embed-text`）。
  - 无外部服务时，降级为**关键词倒排索引**（TF-IDF + 余弦相似度）。
- 存储：使用`bbolt`键值数据库（单文件），存放文档块及其向量/关键词。

------

## 6. Agent核心循环（ReAct with Tools）

go

```
func (a *Agent) Run(ctx context.Context, userInput string) error {
    // 1. 将用户输入添加到消息列表
    a.messages = append(a.messages, Message{Role: "user", Content: userInput})

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        default:
        }

        // 2. 调用LLM（流式），获取响应
        resp, err := a.llm.Chat(ctx, a.messages, a.tools)
        if err != nil { return err }

        // 3. 处理LLM响应
        if resp.StopReason == "tool_calls" {
            // 并行执行所有tool调用（并发）
            results := a.executeToolCalls(ctx, resp.ToolCalls)
            // 将工具结果添加到消息列表
            a.messages = append(a.messages, results...)
            continue // 继续循环
        } else {
            // 最终回答，流式输出到GUI
            a.sendToGUI(resp.Content)
            break
        }
    }
    return nil
}
```



- **流式输出**：LLM响应逐字通过`chan string`发送，GUI实时追加。
- **工具执行**：每个工具调用开启独立goroutine，但需注意并发写文件可能冲突（使用文件锁）。

------

## 7. GUI设计（Fyne）

### 7.1 布局

- **主区域**：`widget.TextGrid`或`widget.Entry`（多行只读），显示对话和AI思考过程。
- **侧边栏**（可折叠）：
  - 文件修改列表（点击可查看diff）。
  - 当前上下文token用量（`tiktoken-go`实时计算）。
- **底部**：多行输入框 + “发送”按钮 + “停止”按钮。
- **状态栏**：当前使用的模型、搜索后端、连接状态。

### 7.2 实时输出区

- 使用`widget.Entry`并设置`Wrapping=fyne.TextWrapOff`，添加滚动容器。
- 接收来自Agent的`chan string`，通过`entry.SetText(entry.Text + newText)`（因Fyne线程安全需调用`fyne.Do`）。

### 7.3 打断实现

- 点击“停止”按钮调用`cancel()`，Agent循环和命令执行立即终止。
- 需在命令执行中检查`ctx.Done()`，并清理子进程（`exec.CommandContext`会自动Kill）。

### 7.4 文件修改跟踪

- 每次写文件操作成功后，将路径添加到`[]string`，并在侧边栏`List`显示。
- 点击项时，弹出对话框显示`diff`（对比修改前后内容，若内存中保留了旧内容）。

------

## 8. Skill加载机制

- 启动时扫描 `./.agent/skills/` 目录。
- 解析每个SKILL.md，提取Frontmatter和正文。
- 在Agent初始化时，将全部skill描述（名称+描述+触发词）作为系统消息的一部分，提醒LLM何时应激活。
- **动态注入**：当用户问题匹配触发词时，将该skill的完整正文追加到系统消息（或作为用户消息前缀）。

------

## 9. 上下文用量监控

- 使用`tiktoken-go`计算当前`messages`列表的token数（按模型对应编码）。
- 在每次LLM调用前后计算，并将数值通过channel发送到GUI状态栏。
- 当接近模型上下文窗口（如80%）时，自动触发摘要压缩（调用LLM总结历史对话）。

------

## 10. 配置与持久化

- 配置文件 `~/.agent/config.json` 存储：
  - API密钥（Brave、LLM等）。
  - 默认搜索后端。
  - 黑名单可编辑。
  - 记忆存储路径。
- 会话历史自动保存至 `~/.agent/sessions/`，支持恢复。

------

## 11. 编译与部署

- 命令：`CGO_ENABLED=0 go build -ldflags="-s -w" -o agent`。
- 资源嵌入：使用`embed`包将默认UI模板（无）和默认配置嵌入二进制。
- 打包图标：`fyne package -os windows -icon icon.ico` 生成可安装包。

------

## 12. 安全性考量

- 命令黑名单可配置，支持正则表达式。
- 危险操作（删除文件、格式化）需用户二次确认（GUI弹窗）。
- 网络请求（搜索）仅使用HTTPS，敏感信息不记录日志。
- 文件操作限制：不允许访问系统目录（如`/etc`，`C:\Windows`），可通过白名单限制。

------

## 13. 开发路线图（约3周）

| 阶段  | 任务                                                  | 工时 |
| :---- | :---------------------------------------------------- | :--- |
| 第1周 | 核心Loop + 基础工具（文件读写/命令） + CLI测试        | 5天  |
| 第2周 | GUI布局 + 实时输出 + 打断 + 上下文监控 + 文件修改跟踪 | 5天  |
| 第3周 | 搜索集成 + Skill加载 + 记忆持久化 + 打包测试          | 5天  |

------

## 14. 示例工具调用格式（LLM需遵循）

json

```
{
  "tool_calls": [
    {
      "id": "call_123",
      "type": "function",
      "function": {
        "name": "read_file_range",
        "arguments": "{\"path\":\"main.go\", \"start_line\":10, \"end_line\":20}"
      }
    }
  ]
}
```



工具响应（返回给LLM）：

json

```
{
  "role": "tool",
  "tool_call_id": "call_123",
  "content": "```go\npackage main\n\nfunc main() {\n    fmt.Println(\"Hello\")\n}\n```"
}
```



------

## 15. 后续扩展可能

- 支持多Agent协作（派生子Agent）。
- 集成代码索引（LSP）提供补全/跳转。