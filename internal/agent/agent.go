package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"go-agent/internal/llm"
	"go-agent/internal/logx"
	"go-agent/internal/memory"
	"go-agent/internal/skill"
	"go-agent/internal/tools"
)

// Options 是 Agent 的运行时回调与参数。
type Options struct {
	// OnContent 收到 LLM 流式文本增量。
	OnContent func(content string)
	// OnToolStart 在某工具开始执行时回调。
	// detail 是工具的可读摘要（如 exec_command 的实际命令文本），非命令工具时为工具名。
	OnToolStart func(name, detail string)
	// OnToolEnd 在某工具执行完毕后回调（无论成功或失败）。
	OnToolEnd func(name string)
	// OnTokenCount 每轮 LLM 调用前回调当前已用/上下文上限 token 数。
	OnTokenCount func(used, limit int)
	// SystemPrompt 追加的自定义系统提示词。
	SystemPrompt string
	// MaxIterations 限制单轮对话内最大工具循环次数，默认 10。
	MaxIterations int
	// ContextWindow 模型上下文窗口 token 数，默认 1000000（DeepSeek V4 系列为 1M）。
	ContextWindow int
	// Skills 已加载的技能列表（按触发词动态注入）。
	Skills []*skill.Skill
	// MemoryLong 长期记忆（bbolt）；nil 表示禁用。
	MemoryLong *memory.LongTerm
}

const defaultSystemPrompt = `你是一个运行在用户项目目录中的编程助手 Agent。你的工作方式是：
1. 分析用户需求，判断是否需要调用工具。
2. 需要时按工具定义发起函数调用（参数必须是合法 JSON）。
3. 观察工具返回的结果，继续推理，直到完成目标。
4. 完成后用中文给出简洁的总结，说明做了什么修改及原因。

工具使用准则：
- 查看代码前先用 search_file_names 定位文件，再用 search_file_content 检索内容，最后按行范围读取，避免一次性读超大文件。
- 修改文件：整文件替换用 write_file，精准局部修改用 edit_file（原内容+新内容）。
- 修改前先读取相关部分理解上下文；写完后可用 search_file_content 或 exec_command 验证。
- exec_command 用于编译、运行测试、查看 git 状态等；命令是实际执行的，必须谨慎。
- 长耗时命令（构建、测试）建议用 exec_command 的 background=true 后台执行，再用 check_command_status 轮询状态直到完成。
- 需要项目文件之外的最新信息时使用 web_search。
- 危险命令（删除、格式化、关机等）会被安全策略拦截。
- 工具输出可能被截断，若需完整内容请缩小范围重新调用。

注意：除非用户明确要求，不要修改与需求无关的文件。`

// Agent 执行 ReAct 循环，并集成技能加载、记忆与上下文监控。
type Agent struct {
	llm      *llm.Client
	tools    *tools.Registry
	messages []llm.Message
	opts     Options
	maxIter  int
	ctxLimit atomic.Int64
}

// New 创建一个 Agent。
func New(llmClient *llm.Client, reg *tools.Registry, opts Options) *Agent {
	if opts.MaxIterations <= 0 {
		opts.MaxIterations = 10
	}
	if opts.ContextWindow <= 0 {
		opts.ContextWindow = 1000000
	}
	a := &Agent{
		llm:     llmClient,
		tools:   reg,
		opts:    opts,
		maxIter: opts.MaxIterations,
	}
	a.ctxLimit.Store(int64(opts.ContextWindow))
	return a
}

// SetContextWindow 动态更新上下文窗口上限（例如启动时探测到服务端实际值）。
func (a *Agent) SetContextWindow(n int) {
	if n > 0 {
		a.ctxLimit.Store(int64(n))
	}
}

// ContextWindow 返回当前上下文窗口上限 token 数。
func (a *Agent) ContextWindow() int { return int(a.ctxLimit.Load()) }

// Messages 返回当前会话的全部消息。
func (a *Agent) Messages() []llm.Message { return a.messages }

// SetMessages 恢复会话历史（用于 /load）。
func (a *Agent) SetMessages(m []llm.Message) { a.messages = m }

// Reset 清空会话历史。
func (a *Agent) Reset() { a.messages = nil }

// buildSystemMessage 根据当前用户输入动态组装系统消息：
// 基础提示词 + 技能摘要 + 触发技能正文 + 长期记忆检索。
func (a *Agent) buildSystemMessage(ctx context.Context, userInput string) string {
	var parts []string
	parts = append(parts, defaultSystemPrompt)
	if a.opts.SystemPrompt != "" {
		parts = append(parts, a.opts.SystemPrompt)
	}

	if len(a.opts.Skills) > 0 {
		var b strings.Builder
		b.WriteString("可用技能（命中触发词时自动应用技能指令）：\n")
		for _, s := range a.opts.Skills {
			b.WriteString(s.Describe() + "\n")
		}
		parts = append(parts, b.String())
		for _, s := range a.opts.Skills {
			if s.Match(userInput) {
				parts = append(parts, "### 技能「"+s.Name+"」指令\n"+s.Body)
			}
		}
	}

	if a.opts.MemoryLong != nil {
		if hits, err := a.opts.MemoryLong.Search(ctx, userInput, 3); err != nil {
			logx.Warn("长期记忆检索失败: %v", err)
		} else if len(hits) > 0 {
			parts = append(parts, "相关历史记忆：\n"+strings.Join(hits, "\n---\n"))
		}
	}
	return strings.Join(parts, "\n\n")
}

// Run 处理一条用户输入，直到模型产出最终回答。
func (a *Agent) Run(ctx context.Context, userInput string) (string, error) {
	// 动态构建/更新系统消息
	sys := a.buildSystemMessage(ctx, userInput)
	if len(a.messages) > 0 && a.messages[0].Role == "system" {
		a.messages[0].Content = sys
	} else {
		a.messages = append([]llm.Message{{Role: "system", Content: sys}}, a.messages...)
	}

	a.messages = append(a.messages, llm.Message{Role: "user", Content: userInput})

	defs := a.toolDefs()

	var finalAnswer string
	for iter := 0; iter < a.maxIter; iter++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		// 上下文用量监控：接近窗口上限时自动压缩历史
		if err := a.maybeCompact(ctx); err != nil && ctx.Err() == nil {
			// 压缩失败不阻断流程，但需上报
			logx.Warn("上下文自动压缩失败: %v", err)
		}

		assistant, err := a.llm.Chat(ctx, a.messages, defs, a.opts.OnContent)
		if err != nil {
			return "", err
		}
		a.messages = append(a.messages, assistant)

		if len(assistant.ToolCalls) == 0 {
			finalAnswer = assistant.Content
			break
		}

		for _, tc := range assistant.ToolCalls {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			result := a.executeTool(ctx, tc)
			a.messages = append(a.messages, llm.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    result,
			})
		}
	}
	if finalAnswer == "" {
		return "", errors.New("达到最大工具循环次数，任务未完成")
	}

	a.recordMemory(ctx, userInput, finalAnswer)
	return finalAnswer, nil
}

// maybeCompact 计算上下文 token 数并上报；超过 80% 时调用 LLM 摘要压缩历史。
func (a *Agent) maybeCompact(ctx context.Context) error {
	limit := a.ctxLimit.Load()
	used := llm.CountMessages(a.messages)
	if a.opts.OnTokenCount != nil {
		a.opts.OnTokenCount(used, int(limit))
	}
	if used < int(float64(limit)*0.8) {
		return nil
	}
	return a.compactHistory(ctx)
}

// compactHistory 将除首条 system 外的历史压缩为摘要，保留最近 4 条消息。
func (a *Agent) compactHistory(ctx context.Context) error {
	idx := 0
	if len(a.messages) > 0 && a.messages[0].Role == "system" {
		idx = 1
	}
	keep := a.messages[:idx]
	rest := a.messages[idx:]
	if len(rest) < 8 {
		return nil
	}

	var b strings.Builder
	for _, m := range rest[:len(rest)-4] {
		switch m.Role {
		case "user":
			b.WriteString("用户: " + m.Content + "\n")
		case "assistant":
			b.WriteString("助手: " + m.Content + "\n")
		case "tool":
			b.WriteString("[工具结果] " + m.Content + "\n")
		}
	}
	prompt := "请用中文简要总结以下对话中已完成的工作和关键信息（不超过200字），供后续会话继续使用：\n\n" + b.String()
	sumMsg := []llm.Message{{Role: "user", Content: prompt}}
	resp, err := a.llm.Chat(ctx, sumMsg, nil, nil)
	if err != nil {
		return err
	}

	sum := llm.Message{Role: "user", Content: "以下是之前的对话摘要，请基于此继续：\n" + resp.Content}
	keepLast := rest[len(rest)-4:]
	a.messages = append(keep, append([]llm.Message{sum}, keepLast...)...)
	return nil
}

// recordMemory 将本轮对话写入长期记忆。
func (a *Agent) recordMemory(ctx context.Context, userInput, answer string) {
	if a.opts.MemoryLong == nil {
		return
	}
	if ctx.Err() != nil {
		return
	}
	// 截断过长的回答，避免记忆膨胀
	text := "用户: " + userInput + "\n助手: " + truncateStr(answer, 1000)
	if err := a.opts.MemoryLong.Add(text); err != nil {
		logx.Warn("长期记忆写入失败: %v", err)
	}
}

// executeTool 执行单个工具调用并返回结果文本。
func (a *Agent) executeTool(ctx context.Context, tc llm.ToolCall) string {
	name := tc.Function.Name

	var args map[string]any
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil || args == nil {
		args = map[string]any{}
	}

	tool, ok := a.tools.Get(name)
	if !ok {
		return fmt.Sprintf("错误：未知工具 %q，可用工具：%s", name, a.availableNames())
	}

	// detail 供 UI 展示：exec_command 等带 command 参数的工具显示实际命令，其余用工具名。
	detail := name
	if cmd, ok := args["command"].(string); ok && strings.TrimSpace(cmd) != "" {
		detail = strings.TrimSpace(cmd)
	}

	if a.opts.OnToolStart != nil {
		a.opts.OnToolStart(name, detail)
	}
	result, err := tool.Execute(ctx, args)
	if a.opts.OnToolEnd != nil {
		a.opts.OnToolEnd(name)
	}
	if err != nil {
		return "错误: " + err.Error()
	}
	return result
}

func (a *Agent) availableNames() string {
	names := make([]string, 0, len(a.tools.List()))
	for _, t := range a.tools.List() {
		names = append(names, t.Name())
	}
	return strings.Join(names, ", ")
}

// toolDefs 将注册表转换为 LLM 的 tools 定义。
func (a *Agent) toolDefs() []llm.ToolDef {
	list := a.tools.List()
	defs := make([]llm.ToolDef, 0, len(list))
	for _, t := range list {
		defs = append(defs, llm.ToolDef{
			Type: "function",
			Function: llm.FunctionDef{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.ArgsSchema(),
			},
		})
	}
	return defs
}

func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
