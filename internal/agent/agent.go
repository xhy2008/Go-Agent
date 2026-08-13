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
	// OnUsage 每轮 LLM 调用完成后回调服务端报告的真实缓存命中/未命中 token 数（费用监控）。
	OnUsage func(hit, miss int)
	// OnAskContinue 工具调用次数达到 MaxToolCalls 上限时回调，询问用户是否继续。
	// 返回 true 则清零计数继续执行；false 则中止本轮。为 nil 时达到上限直接中止。
	OnAskContinue func(used int) bool
	// OnTaskDone 一轮任务结束后回调（无论成功、出错或被中断），
	// 用于触发 codegraph 后台增量重建索引。实现应尽快返回（自行启动 goroutine）。
	OnTaskDone func()
	// SystemPrompt 追加的自定义系统提示词。
	SystemPrompt string
	// MaxToolCalls 限制每条用户输入内的最大工具调用次数（每次用户输入后从 0 重新累计），
	// 默认 200。达到上限后暂停执行，经 OnAskContinue 询问用户是否继续。
	MaxToolCalls int
	// ContextWindow 模型上下文窗口 token 数，默认 1000000（DeepSeek V4 系列为 1M）。
	ContextWindow int
	// Skills 已加载的技能列表（按触发词动态注入）。
	Skills []*skill.Skill
}

const defaultSystemPrompt = `你是一个运行在用户项目目录中的编程助手 Agent。你的工作方式是：
1. 分析用户需求，判断是否需要调用工具。
2. 需要时按工具定义发起函数调用（参数必须是合法 JSON）。
3. 观察工具返回的结果，继续推理，直到完成目标。
4. 完成后用中文给出简洁的总结，说明做了什么修改及原因。

工具使用准则：
- 查看代码前先用 search_file_names 定位文件，再用 search_file_content 检索内容，最后按行范围读取，避免一次性读超大文件。
- 修改前先读取相关部分理解上下文；写完后可用 search_file_content 或 exec_command 验证。
- 长耗时命令（构建、测试）建议用 exec_command 的 background=true 后台执行，再用 check_command_status 轮询状态直到完成。
- 工具输出可能被截断，若需完整内容请缩小范围重新调用。
- 不要修改与用户需求无关的文件，不要写多余代码。

长期记忆：
- 用文件工具维护项目根目录的 MEMORY.md 作为长期记忆。
- 对话中出现值得长期保留的信息（用户偏好、项目约定、关键决策）时，更新 MEMORY.md；处理相关任务前先读取它。`

// Agent 执行 ReAct 循环，并集成技能加载与上下文监控。
type Agent struct {
	llm      *llm.Client
	tools    *tools.Registry
	messages []llm.Message
	opts     Options
	maxTools int
	ctxLimit atomic.Int64
	// sysCache 是构建一次后缓存的稳定系统提示词（不含任何随输入变化的内容）。
	// 工具定义由请求的 tools 字段提供，长期记忆由 Agent 用文件工具自行维护，
	// 因此系统提示词可保持恒定，保证请求前缀稳定以命中服务端上下文缓存。
	sysCache string
}

// New 创建一个 Agent。
func New(llmClient *llm.Client, reg *tools.Registry, opts Options) *Agent {
	if opts.MaxToolCalls <= 0 {
		opts.MaxToolCalls = 200
	}
	if opts.ContextWindow <= 0 {
		opts.ContextWindow = 1000000
	}
	a := &Agent{
		llm:      llmClient,
		tools:    reg,
		opts:     opts,
		maxTools: opts.MaxToolCalls,
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

// SetSkills 热更新技能列表（用于重新扫描 .agent/skills/ 后不重启即生效）。
func (a *Agent) SetSkills(skills []*skill.Skill) {
	a.opts.Skills = skills
	a.sysCache = "" // 技能列表变化会导致系统提示词重建，需清除缓存
}

// SystemPrompt 返回发送给模型的系统提示词全文（调试用）。
// 内容构建后保持恒定（仅 SetSkills 触发重建），不含任何随输入变化的部分，
// 从而保证请求前缀稳定、可命中服务端上下文缓存。
func (a *Agent) SystemPrompt() string { return a.sysOnce() }

// ContextWindow 返回当前上下文窗口上限 token 数。
func (a *Agent) ContextWindow() int { return int(a.ctxLimit.Load()) }

// Messages 返回当前会话的全部消息。
func (a *Agent) Messages() []llm.Message { return a.messages }

// SetMessages 恢复会话历史（用于 /load）。
func (a *Agent) SetMessages(m []llm.Message) { a.messages = m }

// Reset 清空会话历史。
func (a *Agent) Reset() { a.messages = nil }

// sysOnce 构建一次并缓存稳定的系统提示词：基础提示词 + 自定义提示词 + 常驻技能摘要。
// 工具定义由请求的 tools 字段提供（冗余注入反而增加前缀长度），
// 长期记忆由 Agent 通过文件工具自行维护（见 defaultSystemPrompt），均不再注入。
// 非 code 分类的技能不常驻，模型用 list_skills 查询目录、read_skill 读取指令，
// 从而把常驻前缀控制在最小，保证前缀稳定以命中服务端上下文缓存。
func (a *Agent) sysOnce() string {
	if a.sysCache != "" {
		return a.sysCache
	}
	var parts []string
	parts = append(parts, defaultSystemPrompt)
	if a.opts.SystemPrompt != "" {
		parts = append(parts, a.opts.SystemPrompt)
	}

	var resident []*skill.Skill
	for _, s := range a.opts.Skills {
		if s.IsResident() {
			resident = append(resident, s)
		}
	}
	if len(resident) > 0 {
		var b strings.Builder
		b.WriteString("可用技能（需要完整指令时，调用 read_skill 工具并传入技能名）：\n")
		for _, s := range resident {
			b.WriteString(s.Describe() + "\n")
		}
		parts = append(parts, b.String())
	}
	if len(a.opts.Skills) > len(resident) {
		parts = append(parts, "存在未列出的技能（与代码无关，如文档处理、GitHub、研究、工作流等）。需要此类能力时，先用 list_skills 工具查询技能目录，确定技能名后再调用 read_skill 获取完整指令。")
	}

	a.sysCache = strings.Join(parts, "\n\n")
	return a.sysCache
}

// Run 处理一条用户输入，直到模型产出最终回答。
func (a *Agent) Run(ctx context.Context, userInput string) (string, error) {
	// 无论成功、出错或被中断，任务结束后均触发一次（由实现方决定是否异步）。
	if a.opts.OnTaskDone != nil {
		defer a.opts.OnTaskDone()
	}

	// 使用稳定的系统提示词（构建一次后恒定）
	sys := a.sysOnce()
	if len(a.messages) > 0 && a.messages[0].Role == "system" {
		a.messages[0].Content = sys
	} else {
		a.messages = append([]llm.Message{{Role: "system", Content: sys}}, a.messages...)
	}

	a.messages = append(a.messages, llm.Message{Role: "user", Content: userInput})

	defs := a.toolDefs()

	var finalAnswer string
	toolUsed := 0 // 本次用户输入内已执行的工具调用次数，每次 Run 从 0 重新累计
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		// 上下文用量监控：接近窗口上限时自动压缩历史
		if err := a.maybeCompact(ctx); err != nil && ctx.Err() == nil {
			// 压缩失败不阻断流程，但需上报
			logx.Warn("上下文自动压缩失败: %v", err)
		}

		assistant, err := a.llm.Chat(ctx, a.messages, defs, a.opts.OnContent, a.opts.OnUsage)
		if err != nil && assistant.Content != "" {
			// 有已生成的输出时，中断也保留：避免已完成的输出丢失，用户可续写。
			switch {
			case errors.Is(err, context.Canceled):
				// 用户手动打断：保留部分内容，并附加系统提示说明输出不完整，
				// 后续模型能据此识别“已输出部分”与“被打断”的事实。
				a.messages = append(a.messages, llm.Message{Role: "assistant", Content: assistant.Content})
				a.messages = append(a.messages, llm.Message{
					Role:    "system",
					Content: "上一轮助手回复被用户手动打断，输出不完整。若用户要求继续，请从已生成内容的末尾续写；否则忽略此条。",
				})
				return "", err // 保持 context canceled 语义，UI 显示“已停止”
			case errors.Is(err, llm.ErrStreamInterrupted):
				// 断网/超时/提前关闭：半截工具调用参数不可执行，只保留纯文本部分。
				a.messages = append(a.messages, llm.Message{Role: "assistant", Content: assistant.Content})
				return "", fmt.Errorf("流式输出被中断（已保留已生成内容，可发送“继续”续写）：%w", err)
			}
		}
		if err != nil {
			return "", err
		}
		a.messages = append(a.messages, assistant)

		if len(assistant.ToolCalls) == 0 {
			finalAnswer = assistant.Content
			break
		}

		for _, tc := range assistant.ToolCalls {
			// 达到本轮工具调用上限：暂停询问用户是否继续；继续则清零从零重新累计。
			if toolUsed >= a.maxTools {
				if !a.askContinue(toolUsed) {
					return "", fmt.Errorf("已达到最大工具调用次数（%d），任务已停止", toolUsed)
				}
				toolUsed = 0
			}
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			result := a.executeTool(ctx, tc)
			toolUsed++
			a.messages = append(a.messages, llm.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    result,
			})
		}
	}
	return finalAnswer, nil
}

// askContinue 工具调用次数达到上限时询问用户是否继续；回调未提供时默认停止。
func (a *Agent) askContinue(used int) bool {
	if a.opts.OnAskContinue == nil {
		return false
	}
	return a.opts.OnAskContinue(used)
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
	resp, err := a.llm.Chat(ctx, sumMsg, nil, nil, nil)
	if err != nil {
		return err
	}

	sum := llm.Message{Role: "user", Content: "以下是之前的对话摘要，请基于此继续：\n" + resp.Content}
	keepLast := rest[len(rest)-4:]
	a.messages = append(keep, append([]llm.Message{sum}, keepLast...)...)
	return nil
}

// recordMemory 已移除：长期记忆由 Agent 用文件工具自行维护（见 defaultSystemPrompt），
// 程序不再自动注入或写入记忆。

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
