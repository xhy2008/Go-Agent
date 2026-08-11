package tools

import (
	"context"
	"fmt"

	"go-agent/internal/search"
)

// WebSearchTool 联网搜索工具（封装 search.Manager）。
type WebSearchTool struct {
	Manager *search.Manager
}

func (t *WebSearchTool) Name() string { return "web_search" }
func (t *WebSearchTool) Description() string {
	return "联网搜索互联网。用于查询最新信息、文档、API 用法等本地文件之外的内容。返回标题、链接和摘要。"
}
func (t *WebSearchTool) ArgsSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query":       map[string]any{"type": "string", "description": "搜索关键词"},
			"max_results": map[string]any{"type": "integer", "description": "返回结果数，默认 5"},
		},
		"required": []string{"query"},
	}
}

func (t *WebSearchTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return "", fmt.Errorf("query 不能为空")
	}
	maxResults := 5
	if n, ok := args["max_results"].(float64); ok && int(n) > 0 {
		maxResults = int(n)
	}

	results, err := t.Manager.Search(ctx, query, maxResults)
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "没有找到相关结果", nil
	}
	return search.FormatResults(results), nil
}
