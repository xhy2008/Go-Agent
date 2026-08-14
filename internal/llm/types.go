package llm

// Message 是与 LLM 交互的消息结构（OpenAI 兼容格式）。
type Message struct {
	Role       string     `json:"role"` // system/user/assistant/tool
	Content    string     `json:"content"` // DeepSeek 要求该字段必须存在（可为空串），故不能 omitempty
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	// ReasoningContent 是 DeepSeek V4 思考模式的推理文本（assistant 消息）。
	// 回传规则（见 DeepSeek 官方文档 thinking_mode）：
	//   - 该消息带 tool_calls 时，reasoning_content 必须在后续请求中完整回传（缺失返回 400）；
	//   - 纯文本 assistant 消息不回传（官方建议，减少 token）。
	// 由 agent.sendMessages 在发送前按此规则剥离/保留；会话保存与 GUI 展示始终保留。
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

// ToolCall 是模型发起的一次函数调用。
type ToolCall struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"` // "function"
	Index    int    `json:"index,omitempty"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON 字符串
	} `json:"function"`
}

// ToolDef 是注册给模型的工具描述。
type ToolDef struct {
	Type     string     `json:"type"` // "function"
	Function FunctionDef `json:"function"`
}

// FunctionDef 描述一个函数工具。
type FunctionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}
