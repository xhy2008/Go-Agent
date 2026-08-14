package agent

import (
	"testing"

	"go-agent/internal/llm"
)

// TestSendMessages_ReasoningEcho 验证 DeepSeek V4 思考模式回传规则：
//   - 带 tool_calls 的 assistant 消息必须原样保留 reasoning_content（缺失会返回 400）；
//   - 纯文本 assistant 消息剥离 reasoning_content（官方建议，减少 token）；
//   - 非 assistant 消息不受影响，原列表不被修改。
func TestSendMessages_ReasoningEcho(t *testing.T) {
	a := New(nil, nil, Options{})
	a.SetMessages([]llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "", ReasoningContent: "思考 1",
			ToolCalls: []llm.ToolCall{{ID: "c1", Type: "function"}}},
		{Role: "tool", ToolCallID: "c1", Content: "结果"},
		{Role: "assistant", Content: "最终回答", ReasoningContent: "思考 2"},
	})

	sent := a.sendMessages()
	if len(sent) != 5 {
		t.Fatalf("消息条数 = %d, want 5", len(sent))
	}
	// 带 tool_calls 的 assistant：保留 reasoning
	if got := sent[2].ReasoningContent; got != "思考 1" {
		t.Errorf("带 tool_calls 的 assistant reasoning = %q, want %q（缺失将触发 400）", got, "思考 1")
	}
	// 纯文本 assistant：剥离 reasoning
	if got := sent[4].ReasoningContent; got != "" {
		t.Errorf("纯文本 assistant reasoning = %q, want 空", got)
	}
	// 原列表必须未被修改（会话内始终保留推理文本供展示/恢复）
	if got := a.Messages()[4].ReasoningContent; got != "思考 2" {
		t.Errorf("原列表 reasoning 被修改 = %q, want %q", got, "思考 2")
	}
}
