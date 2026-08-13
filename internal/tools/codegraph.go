package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"go-agent/internal/codegraph"
	"go-agent/internal/semantic"
)

// CodegraphToolSet 是 codegraph 图查询工具的公共依赖：索引访问 + 项目根 + 可选语义重排。
// 各工具（search/node/callers/callees/trace/impact）通过其返回的子集节省 token，
// 而非一次性返回整图。
type CodegraphToolSet struct {
	// Index 返回当前索引（nil 表示未构建完成）；Root 为项目根目录（读取源码用）。
	Index func() *codegraph.Index
	Root  string
	// Semantic 为可选的语义重排服务（nil 时跳过向量重排，仅用 FTS5/图遍历）。
	Semantic *semantic.Service
	// FTS 为可选的 FTS5 检索器（nil 时回退内存词法 Explore）。
	FTS func(query string, limit int) ([]int, error)
}

// currentIndex 返回索引并检查可用性（未构建时返回友好提示）。
func (t *CodegraphToolSet) currentIndex() (*codegraph.Index, error) {
	var ix *codegraph.Index
	if t.Index != nil {
		ix = t.Index()
	}
	if ix == nil {
		return nil, fmt.Errorf("代码图索引未初始化：首次构建进行中或尚未触发。请稍后重试，或先执行一个任务触发自动重建。")
	}
	return ix, nil
}

// search 是 search 工具与内部共用的检索：FTS5 候选 → 可选语义重排 → Match。
func (t *CodegraphToolSet) search(query string, maxResults int) ([]codegraph.Match, error) {
	ix, err := t.currentIndex()
	if err != nil {
		return nil, err
	}
	// FTS5 优先（贴近原版全文检索）；无 FTS 时回退内存词法
	if t.FTS != nil {
		if ids, ferr := t.FTS(query, maxResults*8); ferr == nil && len(ids) > 0 {
			if t.Semantic != nil {
				return t.Semantic.Rerank(ix, t.Root, query, ids, maxResults), nil
			}
			return matchesOf(ix, t.Root, ids, maxResults), nil
		}
	}
	return ix.Explore(t.Root, query, maxResults), nil
}

// matchesOf 按给定 ID 顺序构造 Match。
func matchesOf(ix *codegraph.Index, root string, ids []int, max int) []codegraph.Match {
	out := make([]codegraph.Match, 0, len(ids))
	for _, id := range ids {
		if id < 0 || id >= len(ix.Nodes) {
			continue
		}
		out = append(out, ix.MatchByID(root, id))
		if len(out) >= max {
			break
		}
	}
	return out
}

// resolveExact 将符号名定位为唯一节点 ID：
// 依次尝试 Receiver.Name、Name 唯一、文件名（完整/基名）；无法唯一命中返回 false。
func (t *CodegraphToolSet) resolveExact(sym string) (int, error) {
	ix, err := t.currentIndex()
	if err != nil {
		return -1, err
	}
	s := strings.TrimSpace(sym)
	if s == "" {
		return -1, fmt.Errorf("symbol 不能为空")
	}
	// Receiver.Name 精确（方法/接口方法）
	if i := strings.IndexByte(s, '.'); i > 0 {
		recv, name := s[:i], s[i+1:]
		for _, id := range ix.ByName[name] {
			if ix.Nodes[id].Receiver == recv {
				return id, nil
			}
		}
	}
	// Name 精确唯一
	if ids := ix.ByName[s]; len(ids) == 1 {
		return ids[0], nil
	}
	// 文件（完整相对路径 / 基名）
	base := filepath.Base(s)
	for i := range ix.Nodes {
		n := &ix.Nodes[i]
		if n.File == s || n.File == base || filepath.Base(n.File) == base {
			return n.ID, nil
		}
	}
	return -1, fmt.Errorf("符号 %q 未精确命中；可尝试符号名、方法名（如 Hello.Greet）或文件名", s)
}

// resolveFuzzy 模糊解析：返回名称/文件匹配的候选 ID（供未精确命中时提示）。
func (t *CodegraphToolSet) resolveFuzzy(sym string, limit int) []int {
	if t.Index == nil {
		return nil
	}
	ix := t.Index()
	if ix == nil {
		return nil
	}
	s := strings.ToLower(strings.TrimSpace(sym))
	if s == "" {
		return nil
	}
	var out []int
	seen := map[int]bool{}
	add := func(id int) bool {
		if seen[id] || len(out) >= limit {
			return false
		}
		seen[id] = true
		out = append(out, id)
		return true
	}
	for i := range ix.Nodes {
		n := &ix.Nodes[i]
		if strings.Contains(strings.ToLower(n.Name), s) ||
			strings.Contains(strings.ToLower(n.File), s) ||
			strings.Contains(strings.ToLower(n.Doc), s) {
			if !add(n.ID) && len(out) >= limit {
				break
			}
		}
	}
	return out
}

// CodegraphSearchTool 全文/语义搜索：返回相关符号的紧凑列表（名称/位置/签名/语义分），
// 不附带完整源码，避免浪费 token。
type CodegraphSearchTool struct{ *CodegraphToolSet }

func (t *CodegraphSearchTool) Name() string { return "codegraph_search" }
func (t *CodegraphSearchTool) Description() string {
	return "在代码图中全文/语义搜索符号：输入关键词、符号名或自然语言描述，返回相关符号的名称、位置、签名与相关度。用于定位实现、理解模块构成。"
}
func (t *CodegraphSearchTool) ArgsSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query":       map[string]any{"type": "string", "description": "要搜索的符号名、关键词或自然语言描述"},
			"max_results": map[string]any{"type": "integer", "description": "最多返回的命中数量（默认 5，最大 10）"},
		},
		"required": []string{"query"},
	}
}

func (t *CodegraphSearchTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	query, _ := args["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("query 不能为空")
	}
	max := 5
	if n, ok := args["max_results"].(float64); ok && int(n) > 0 {
		max = int(n)
	}
	if max > 10 {
		max = 10
	}
	ms, err := t.search(query, max)
	if err != nil {
		if strings.Contains(err.Error(), "未初始化") {
			return err.Error(), nil // 未初始化：返回提示而非错误（Agent 可感知）
		}
		return "", err
	}
	if len(ms) == 0 {
		return fmt.Sprintf("未找到与 %q 相关的符号。可尝试其他名称、方法名（如 Hello.Greet）或文件名。", query), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "搜索 %q 命中 %d 条：\n", query, len(ms))
	for i := range ms {
		renderSearchHit(&b, i+1, &ms[i])
	}
	ix := t.Index()
	if ix != nil {
		fmt.Fprintf(&b, "索引构建时间: %s（共 %d 符号 / %d 关系）\n",
			ix.BuiltAt.Format("2006-01-02 15:04:05"), len(ix.Nodes), len(ix.Edges))
	}
	return b.String(), nil
}

// renderSearchHit 渲染单个搜索命中（紧凑，不含源码）。
func renderSearchHit(b *strings.Builder, idx int, m *codegraph.Match) {
	n := &m.Node
	fmt.Fprintf(b, "### %d. %s [%s] (%s:%d)\n", idx, displayName(n), n.Kind, n.File, n.Line)
	if m.Semantic > 0 {
		fmt.Fprintf(b, "语义相关度: %.2f\n", m.Semantic)
	}
	if n.Signature != "" {
		fmt.Fprintf(b, "签名: %s\n", n.Signature)
	}
	if n.Doc != "" {
		fmt.Fprintf(b, "Doc: %s\n", n.Doc)
	}
	b.WriteString("\n")
}

// CodegraphNodeTool 单符号详情：源码 + 直接调用者/被调用者 + 测试覆盖。
type CodegraphNodeTool struct{ *CodegraphToolSet }

func (t *CodegraphNodeTool) Name() string { return "codegraph_node" }
func (t *CodegraphNodeTool) Description() string {
	return "查看单个符号的完整上下文：逐行源码、签名、直接调用者与被调用者、接口实现与测试覆盖。用于深入理解一个函数/方法/类型的实现细节。"
}
func (t *CodegraphNodeTool) ArgsSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"symbol": map[string]any{"type": "string", "description": "符号名、方法名（如 Hello.Greet）或文件名"},
		},
		"required": []string{"symbol"},
	}
}

func (t *CodegraphNodeTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	symbol, _ := args["symbol"].(string)
	id, err := t.resolveExact(symbol)
	if err != nil {
		if cands := t.resolveFuzzy(symbol, 5); len(cands) > 0 {
			var names []string
			for _, c := range cands {
				ix := t.Index()
				if ix != nil && c >= 0 && c < len(ix.Nodes) {
					names = append(names, displayName(&ix.Nodes[c]))
				}
			}
			return err.Error() + fmt.Sprintf("\n相近符号: %s", strings.Join(names, "、")), nil
		}
		return "", err
	}
	ix, err := t.currentIndex()
	if err != nil {
		return "", err
	}
	m := ix.MatchByID(t.Root, id)
	var b strings.Builder
	b.WriteString(renderMatch(1, &m, ix))
	return b.String(), nil
}

// renderMatch 渲染单个命中：符号信息 + 源码 + 调用关系 + 影响面 + 接口实现 + 测试覆盖。
func renderMatch(idx int, m *codegraph.Match, ix *codegraph.Index) string {
	n := &m.Node
	var b strings.Builder
	fmt.Fprintf(&b, "### %d. %s [%s] (%s:%d)\n", idx, displayName(n), n.Kind, n.File, n.Line)
	if n.Doc != "" {
		fmt.Fprintf(&b, "Doc: %s\n", n.Doc)
	}
	if n.Signature != "" {
		fmt.Fprintf(&b, "签名: %s\n", n.Signature)
	}
	if m.Semantic > 0 {
		fmt.Fprintf(&b, "语义相关度: %.2f\n", m.Semantic)
	}
	if len(m.Source) > 0 {
		b.WriteString("```go\n")
		for _, sl := range m.Source {
			fmt.Fprintf(&b, "%4d  %s\n", sl.Num, sl.Text)
		}
		b.WriteString("```\n")
	}
	if len(m.Calls) > 0 {
		b.WriteString("调用:\n")
		for _, e := range m.Calls {
			fmt.Fprintf(&b, "  %s → %s\n", e.Site, nodeLabel(ix, e.To))
		}
	}
	if len(m.Callers) > 0 {
		b.WriteString("被调用于:\n")
		for _, e := range m.Callers {
			fmt.Fprintf(&b, "  %s（来自 %s）\n", e.Site, nodeLabel(ix, e.From))
		}
	}
	if len(m.Blast) > 0 {
		fmt.Fprintf(&b, "影响面（2 跳内 %d 处）:\n", len(m.Blast))
		for _, e := range m.Blast {
			fmt.Fprintf(&b, "  %s → %s\n", e.Site, nodeLabel(ix, e.To))
		}
	}
	if len(m.Impls) > 0 {
		b.WriteString("接口实现:\n")
		for _, e := range m.Impls {
			if e.Kind != codegraph.EdgeImpl {
				continue
			}
			if e.From == n.ID {
				fmt.Fprintf(&b, "  实现者: %s\n", nodeLabel(ix, e.To))
			} else {
				fmt.Fprintf(&b, "  实现自: %s\n", nodeLabel(ix, e.From))
			}
		}
	}
	if len(m.TestRefs) > 0 {
		fmt.Fprintf(&b, "测试覆盖: %d 处位于测试文件\n", len(m.TestRefs))
		for _, e := range m.TestRefs {
			fmt.Fprintf(&b, "  %s\n", e.Site)
		}
	} else {
		b.WriteString("⚠️ 无测试覆盖\n")
	}
	if isInterfaceMethod(ix, n) {
		b.WriteString("⚠️ 动态分派：本符号为接口方法，调用方可能经由接口（详见实现者）\n")
	}
	b.WriteString("\n")
	return b.String()
}

// isInterfaceMethod 判断方法节点是否为接口方法（接收者是对应的接口类型）。
func isInterfaceMethod(ix *codegraph.Index, n *codegraph.Node) bool {
	if n.Kind != codegraph.KindMethod || n.Receiver == "" {
		return false
	}
	for _, id := range ix.ByName[n.Receiver] {
		if ix.Nodes[id].Kind == codegraph.KindInterface {
			return true
		}
	}
	return false
}

// displayName 生成符号的显示名（方法带接收者）。
func displayName(n *codegraph.Node) string {
	if n.Kind == codegraph.KindMethod && n.Receiver != "" {
		return n.Receiver + "." + n.Name
	}
	return n.Name
}

func nodeLabel(ix *codegraph.Index, id int) string {
	if id < 0 || id >= len(ix.Nodes) {
		return "?"
	}
	n := &ix.Nodes[id]
	return fmt.Sprintf("%s (%s:%d)", displayName(n), n.File, n.Line)
}
