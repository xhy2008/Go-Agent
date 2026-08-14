package tools

import (
	"context"
	"sort"
)

// Tool 是 Agent 可调用的工具接口。
type Tool interface {
	Name() string
	Description() string
	// ArgsSchema 返回 JSON Schema 风格的参数定义（properties）。
	ArgsSchema() map[string]any
	Execute(ctx context.Context, args map[string]any) (string, error)
}

// Registry 管理已注册的工具。
type Registry struct {
	tools map[string]Tool
}

// NewRegistry 创建空的工具注册表。
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register 注册一个工具。
func (r *Registry) Register(t Tool) { r.tools[t.Name()] = t }

// Get 按名称获取工具。
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// List 返回全部已注册工具，按名称排序。
// 排序保证每次请求的 tools 定义顺序一致：DeepSeek 上下文缓存以请求前缀为键，
// map 迭代顺序随机会让 tools 字段每次变化，导致缓存全部失效（命中价差 50 倍）。
func (r *Registry) List() []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}
