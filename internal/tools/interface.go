package tools

import "context"

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

// List 返回全部已注册工具。
func (r *Registry) List() []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}
