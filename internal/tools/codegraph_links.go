package tools

import (
	"context"
	"fmt"
	"strings"

	"go-agent/internal/codegraph"
)

// depthArg 解析 depth 参数（默认 def，上限 max）。
func depthArg(args map[string]any, key string, def, max int) int {
	d := def
	if v, ok := args[key].(float64); ok && int(v) > 0 {
		d = int(v)
	}
	if d > max {
		d = max
	}
	return d
}

// renderEdges 渲染边列表：site  From → To。
func renderEdges(b *strings.Builder, ix *codegraph.Index, title string, edges []codegraph.Edge) {
	if len(edges) == 0 {
		fmt.Fprintf(b, "%s: 无\n", title)
		return
	}
	fmt.Fprintf(b, "%s（%d 条）:\n", title, len(edges))
	for _, e := range edges {
		fmt.Fprintf(b, "  %s  %s → %s\n", e.Site, nodeLabel(ix, e.From), nodeLabel(ix, e.To))
	}
}

// CodegraphCallersTool 调用者链（入边递归，深度可调）。
type CodegraphCallersTool struct{ *CodegraphToolSet }

func (t *CodegraphCallersTool) Name() string { return "codegraph_callers" }
func (t *CodegraphCallersTool) Description() string {
	return "列出调用目标符号的函数/方法，可递归展开多级调用者链（默认 1 级，最大 3 级）。用于回答「哪些地方调用了 X」及追溯调用源头。"
}
func (t *CodegraphCallersTool) ArgsSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"symbol": map[string]any{"type": "string", "description": "符号名或方法名（如 Hello.Greet）"},
			"depth":  map[string]any{"type": "integer", "description": "递归深度（默认 1，最大 3）"},
			"max_results": map[string]any{
				"type": "integer", "description": "最多返回的调用者条目（默认 20，最大 50）",
			},
		},
		"required": []string{"symbol"},
	}
}

func (t *CodegraphCallersTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	id, err := t.resolveExact(argSymbol(args))
	if err != nil {
		return "", err
	}
	ix, err := t.currentIndex()
	if err != nil {
		return "", err
	}
	depth := depthArg(args, "depth", 1, 3)
	edges := ix.CallersOf(id, depth)
	edges = capEdges(edges, argMax(args, 20, 50))
	var b strings.Builder
	fmt.Fprintf(&b, "%s 的调用者链（深度 %d）：\n", displayName(&ix.Nodes[id]), depth)
	renderEdges(&b, ix, "调用者（From → 目标）", edges)
	return b.String(), nil
}

// CodegraphCalleesTool 被调用链（出边递归，深度可调）。
type CodegraphCalleesTool struct{ *CodegraphToolSet }

func (t *CodegraphCalleesTool) Name() string { return "codegraph_callees" }
func (t *CodegraphCalleesTool) Description() string {
	return "列出目标符号调用的函数/方法，可递归展开多级被调用链（默认 1 级，最大 3 级）。用于回答「X 内部调用了哪些函数」及追踪依赖。"
}
func (t *CodegraphCalleesTool) ArgsSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"symbol": map[string]any{"type": "string", "description": "符号名或方法名（如 Hello.Greet）"},
			"depth":  map[string]any{"type": "integer", "description": "递归深度（默认 1，最大 3）"},
			"max_results": map[string]any{
				"type": "integer", "description": "最多返回的被调用条目（默认 20，最大 50）",
			},
		},
		"required": []string{"symbol"},
	}
}

func (t *CodegraphCalleesTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	id, err := t.resolveExact(argSymbol(args))
	if err != nil {
		return "", err
	}
	ix, err := t.currentIndex()
	if err != nil {
		return "", err
	}
	depth := depthArg(args, "depth", 1, 3)
	edges := ix.BlastRadius(id, depth)
	edges = capEdges(edges, argMax(args, 20, 50))
	var b strings.Builder
	fmt.Fprintf(&b, "%s 的被调用链（深度 %d）：\n", displayName(&ix.Nodes[id]), depth)
	renderEdges(&b, ix, "被调用（From → To）", edges)
	return b.String(), nil
}

// CodegraphTraceTool 深度调用链：沿出入边双向 BFS（默认深度 3，贴近原版 trace）。
type CodegraphTraceTool struct{ *CodegraphToolSet }

func (t *CodegraphTraceTool) Name() string { return "codegraph_trace" }
func (t *CodegraphTraceTool) Description() string {
	return "追踪符号的完整调用链：同时展开调用者（上游）与被调用者（下游），默认深度 3 级。用于理解一次调用的来龙去脉与数据流经的完整路径。"
}
func (t *CodegraphTraceTool) ArgsSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"symbol": map[string]any{"type": "string", "description": "符号名或方法名（如 Hello.Greet）"},
			"depth":  map[string]any{"type": "integer", "description": "双向递归深度（默认 3，最大 5）"},
			"max_results": map[string]any{
				"type": "integer", "description": "最多返回的调用链条目（默认 40，最大 100）",
			},
		},
		"required": []string{"symbol"},
	}
}

func (t *CodegraphTraceTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	id, err := t.resolveExact(argSymbol(args))
	if err != nil {
		return "", err
	}
	ix, err := t.currentIndex()
	if err != nil {
		return "", err
	}
	depth := depthArg(args, "depth", 3, 5)
	edges := ix.Trace(id, depth)
	edges = capEdges(edges, argMax(args, 40, 100))
	var b strings.Builder
	fmt.Fprintf(&b, "%s 的调用链（双向深度 %d）：\n", displayName(&ix.Nodes[id]), depth)
	renderEdges(&b, ix, "调用链边", edges)
	return b.String(), nil
}

// CodegraphImpactTool 影响面（blast radius）：出边 BFS，评估改动波及范围。
type CodegraphImpactTool struct{ *CodegraphToolSet }

func (t *CodegraphImpactTool) Name() string { return "codegraph_impact" }
func (t *CodegraphImpactTool) Description() string {
	return "评估修改目标符号的影响面（blast radius）：沿调用链向外展开被影响的下游符号（默认 2 级，最大 5 级）。用于改动前评估风险、定位可能被破坏的调用方与下游逻辑。"
}
func (t *CodegraphImpactTool) ArgsSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"symbol": map[string]any{"type": "string", "description": "符号名或方法名（如 Hello.Greet）"},
			"depth":  map[string]any{"type": "integer", "description": "影响面展开深度（默认 2，最大 5）"},
			"max_results": map[string]any{
				"type": "integer", "description": "最多返回的影响条目（默认 40，最大 100）",
			},
		},
		"required": []string{"symbol"},
	}
}

func (t *CodegraphImpactTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	id, err := t.resolveExact(argSymbol(args))
	if err != nil {
		return "", err
	}
	ix, err := t.currentIndex()
	if err != nil {
		return "", err
	}
	depth := depthArg(args, "depth", 2, 5)
	edges := ix.BlastRadius(id, depth)
	edges = capEdges(edges, argMax(args, 40, 100))
	var b strings.Builder
	fmt.Fprintf(&b, "%s 的影响面（blast radius，深度 %d）：\n", displayName(&ix.Nodes[id]), depth)
	renderEdges(&b, ix, "下游影响（From → To）", edges)
	return b.String(), nil
}

func argSymbol(args map[string]any) string {
	s, _ := args["symbol"].(string)
	return s
}

func argMax(args map[string]any, def, max int) int {
	n := def
	if v, ok := args["max_results"].(float64); ok && int(v) > 0 {
		n = int(v)
	}
	if n > max {
		n = max
	}
	return n
}

func capEdges(edges []codegraph.Edge, n int) []codegraph.Edge {
	if len(edges) > n {
		return edges[:n]
	}
	return edges
}
