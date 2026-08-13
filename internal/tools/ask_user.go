package tools

import (
	"context"
	"fmt"
	"strings"
)

// AskUserTool 向用户提问并等待回答（需求确认、方案选择、危险操作征询等）。
type AskUserTool struct {
	// Ask 询问用户并返回回答；返回空串表示用户未回答/取消。
	Ask func(question string) string
}

// NewAskUserTool 创建向用户提问的工具。
func NewAskUserTool(ask func(question string) string) *AskUserTool {
	return &AskUserTool{Ask: ask}
}

func (t *AskUserTool) Name() string { return "ask_user" }
func (t *AskUserTool) Description() string {
	return "向用户提出一个问题并等待回答。用于需求不明确、有多种方案需要用户选择、或执行有风险的操作前征询确认。问题应简短具体，一次只问一个问题。"
}
func (t *AskUserTool) ArgsSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question": map[string]any{"type": "string", "description": "要向用户提出的问题"},
		},
		"required": []string{"question"},
	}
}

func (t *AskUserTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	q, _ := args["question"].(string)
	q = strings.TrimSpace(q)
	if q == "" {
		return "", fmt.Errorf("question 不能为空")
	}
	if t.Ask == nil {
		return "（未配置向用户提问的通道）", nil
	}
	ans := strings.TrimSpace(t.Ask(q))
	if ans == "" {
		return "用户未回答（对话框已取消）。请基于现有信息继续，或换一种更简单的方式完成任务。", nil
	}
	return "用户回答：" + ans, nil
}
