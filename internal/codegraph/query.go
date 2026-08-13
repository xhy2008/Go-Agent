package codegraph

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SourceLine 是一行源码（带行号）。
type SourceLine struct {
	Num  int
	Text string
}

// Match 是一次查询的命中结果（含源码、调用关系与测试覆盖信息）。
type Match struct {
	Node     Node
	Score    int
	Semantic float32 // 语义相似度（-1~1；未启用语义检索时为 0）
	Source   []SourceLine
	Callers  []Edge // 直接调用者（入边 EdgeCall）
	Calls    []Edge // 直接调用（出边 EdgeCall）
	Refs     []Edge // 引用本符号的边（入边 EdgeRef）
	Impls    []Edge // 接口实现边（EdgeImpl，双向相关）
	TestRefs []Edge // 来自 *_test.go 的引用/调用（测试覆盖提示）
	Blast    []Edge // 出边 BFS 2 跳影响面
}

// MatchByID 按节点 ID 构造完整 Match：源码行 + 调用/引用/实现/测试边 + 影响面。
// 供 FTS5 检索（search）与图遍历（node/callers/callees/trace/impact）工具复用。
func (ix *Index) MatchByID(root string, id int) Match {
	if ix == nil || id < 0 || id >= len(ix.Nodes) {
		return Match{}
	}
	m := Match{Node: ix.Nodes[id]}
	m.Source = ix.SourceLines(root, id)
	m.Callers, m.Calls, m.Refs, m.Impls, m.TestRefs = ix.EdgesOf(id)
	m.Blast = ix.BlastRadius(id, 2)
	return m
}

// Explore 按 query 检索符号并按相关度排序，返回最多 maxResults 条命中。
// root 为项目根（读取源码用）。query 支持符号名、Receiver.Name、pkg.Name 与文件名关键词。
// 注意：这是内存词法打分回退路径；生产环境优先用 Store.DB().Search（FTS5）+ 可选语义重排。
func (ix *Index) Explore(root, query string, maxResults int) []Match {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" || ix == nil {
		return nil
	}
	words := splitWords(q)
	if len(words) == 0 {
		words = []string{q}
	}
	last := words[len(words)-1]                 // 限定名/复合查询的最后一段
	raw := splitWords(strings.TrimSpace(query)) // 原始大小写，供精确匹配加分

	type scored struct {
		id    int
		score int
	}
	scoreds := make([]scored, 0, 16)
	for i := range ix.Nodes {
		if s := scoreNode(&ix.Nodes[i], q, last, words, raw); s > 0 {
			scoreds = append(scoreds, scored{i, s})
		}
	}
	if len(scoreds) == 0 {
		return nil
	}
	sort.Slice(scoreds, func(a, b int) bool {
		if scoreds[a].score != scoreds[b].score {
			return scoreds[a].score > scoreds[b].score
		}
		return scoreds[a].id < scoreds[b].id
	})
	if len(scoreds) > maxResults {
		scoreds = scoreds[:maxResults]
	}

	out := make([]Match, 0, len(scoreds))
	for _, s := range scoreds {
		m := Match{Node: ix.Nodes[s.id], Score: s.score}
		m.Source = ix.SourceLines(root, ix.Nodes[s.id].ID)
		m.Callers, m.Calls, m.Refs, m.Impls, m.TestRefs = ix.EdgesOf(ix.Nodes[s.id].ID)
		m.Blast = ix.BlastRadius(ix.Nodes[s.id].ID, 2)
		out = append(out, m)
	}
	return out
}

// splitWords 将查询拆分为标识符词（字母/数字/下划线，保留大小写与原始字符串一致）。
func splitWords(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_')
	})
}

// scoreNode 词法打分。优先级：整串精确(100) > 方法限定名(95) > 文件全名(90) >
// 每词主匹配(末段 88 / 限定名 85 / 文件名基名 80 / 前缀 60 / 子串 40) > 文件包含(30)。
// 与任一查询词大小写完全一致时 +10（Go 符号大小写敏感）。
// 多词查询（如 "Agent.Run executeTool"）每个词都参与主匹配，取最佳得分；
// 弱命中按其余词累加 name/Doc 相关度；全无命中时任一关键词命中 name/Doc 即兜底。
func scoreNode(n *Node, q, last string, words, rawWords []string) int {
	name := strings.ToLower(n.Name)
	recv := strings.ToLower(n.Receiver)
	file := strings.ToLower(n.File)
	fileBase := strings.TrimSuffix(filepath.Base(file), ".go")
	doc := strings.ToLower(n.Doc)

	score := 0
	if name == q && q != "" {
		score = 100
	} else if recv != "" && recv+"."+name == q {
		score = 95
	} else if file == q {
		score = 90
	} else {
		// 多词查询：每个词做主匹配，取最佳
		for _, w := range words {
			s := 0
			switch {
			case name == w:
				s = 88
			case recv != "" && recv+"."+name == w:
				s = 85
			case fileBase == w:
				s = 80
			case strings.HasPrefix(name, w):
				s = 60
			case strings.Contains(name, w):
				s = 40
			}
			if s > score {
				score = s
			}
		}
		if score == 0 && strings.Contains(file, q) {
			score = 30
		}
	}
	// 大小写精确匹配优先（如 Run 应排在 run 之前）
	if score > 0 {
		for _, w := range rawWords {
			if n.Name == w {
				score += 10
				break
			}
		}
	}
	// 多词查询的辅助词：对弱命中（低于限定名级别）累加 name/Doc 相关度
	if score > 0 && score < 88 {
		for _, w := range words {
			if w == last {
				continue
			}
			if strings.Contains(name, w) {
				score += 12
			}
			if strings.Contains(doc, w) {
				score += 8
			}
		}
	}
	// 名称/文件均未命中时：任一关键词命中 name/Doc 即兜底（自然语言查询）
	if score == 0 {
		for _, w := range words {
			if strings.Contains(name, w) || strings.Contains(doc, w) {
				score = 15
				break
			}
		}
	}
	return score
}

// EdgesOf 汇总某符号相关的各类边（只读，供语义层按 ID 构造 Match）。
func (ix *Index) EdgesOf(id int) (callers, calls, refs, impls, testRefs []Edge) {
	for i := range ix.Edges {
		e := ix.Edges[i]
		if e.To == id {
			switch e.Kind {
			case EdgeCall:
				callers = append(callers, e)
			case EdgeRef:
				refs = append(refs, e)
			case EdgeImpl:
				// 该接口方法被以下方法实现
				impls = append(impls, e)
			}
			if isTestSite(e.Site) {
				testRefs = append(testRefs, e)
			}
		}
		if e.From == id && e.Kind == EdgeImpl {
			impls = append(impls, e)
		}
		if e.From == id && e.Kind == EdgeCall {
			calls = append(calls, e)
		}
	}
	return callers, calls, refs, impls, testRefs
}

// BlastRadius 沿出边 EdgeCall BFS 至多 depth 跳，返回路径边（用于影响面）。
func (ix *Index) BlastRadius(id, depth int) []Edge {
	return ix.bfsEdges(id, depth, true)
}

// CallersOf 沿入边 EdgeCall BFS 至多 depth 跳，返回调用者路径边（由近及远）。
func (ix *Index) CallersOf(id, depth int) []Edge {
	return ix.bfsEdges(id, depth, false)
}

// Trace 沿出入边 EdgeCall 双向 BFS 至多 depth 跳，返回调用链路径边（去重，层序）。
func (ix *Index) Trace(id, depth int) []Edge {
	visited := map[int]bool{id: true}
	queue := []int{id}
	var edges []Edge
	for len(queue) > 0 && depth > 0 {
		var next []int
		for _, cur := range queue {
			for _, e := range ix.Edges {
				if e.Kind != EdgeCall {
					continue
				}
				var nxt int
				switch {
				case e.From == cur:
					nxt = e.To // 下游被调用方
				case e.To == cur:
					nxt = e.From // 上游调用方
				default:
					continue
				}
				if visited[nxt] {
					continue
				}
				visited[nxt] = true
				edges = append(edges, e)
				next = append(next, nxt)
			}
		}
		queue = next
		depth--
	}
	return edges
}

// bfsEdges 沿调用边（出或入）BFS 至多 depth 跳。out=true 走出边（被调用方），
// out=false 走入边（调用方）。visited 按前进方向的目标节点去重。
func (ix *Index) bfsEdges(id, depth int, out bool) []Edge {
	visited := map[int]bool{id: true}
	queue := []int{id}
	var edges []Edge
	for len(queue) > 0 && depth > 0 {
		var next []int
		for _, cur := range queue {
			for _, e := range ix.Edges {
				if e.Kind != EdgeCall {
					continue
				}
				var nxt int
				if out {
					if e.From != cur {
						continue
					}
					nxt = e.To
				} else {
					if e.To != cur {
						continue
					}
					nxt = e.From
				}
				if visited[nxt] {
					continue
				}
				visited[nxt] = true
				edges = append(edges, e)
				next = append(next, nxt)
			}
		}
		queue = next
		depth--
	}
	return edges
}

// SourceLines 从磁盘读取符号声明附近的源码（前 2 行到后 8 行）。
func (ix *Index) SourceLines(root string, id int) []SourceLine {
	n := &ix.Nodes[id]
	path := filepath.Join(root, filepath.FromSlash(n.File))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	start := n.Line - 2
	if start < 1 {
		start = 1
	}
	end := n.Line + 8
	if end > len(lines) {
		end = len(lines)
	}
	out := make([]SourceLine, 0, end-start+1)
	for i := start; i <= end; i++ {
		out = append(out, SourceLine{Num: i, Text: strings.TrimSuffix(lines[i-1], "\r")})
	}
	return out
}

// isTestSite 判断引用位置是否位于测试文件。
func isTestSite(site string) bool {
	i := strings.LastIndexByte(site, ':')
	if i < 0 {
		return false
	}
	return strings.HasSuffix(site[:i], "_test.go")
}
