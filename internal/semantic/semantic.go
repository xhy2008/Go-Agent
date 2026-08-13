// Package semantic 提供代码符号的语义检索：基于 llama.cpp embedding 为索引
// 生成全量符号向量，并在词法查询基础上做余弦相似度重排。
// 仅当显式提供 embedding 模型时启用；未启用时回退纯词法 Explore。
package semantic

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"go-agent/internal/codegraph"
	"go-agent/internal/embed"
)

// Service 封装 embedding 模型与语义检索（并发安全）。
type Service struct {
	mu    sync.Mutex
	model *embed.Model
}

// Load 加载 GGUF embedding 模型（nomic-embed-text 用 PoolLast；bge 系列用 PoolCLS）。
func Load(modelPath string, pooling embed.Pooling) (*Service, error) {
	m, err := embed.Load(modelPath, pooling)
	if err != nil {
		return nil, err
	}
	return &Service{model: m}, nil
}

// Close 释放模型。
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.model != nil {
		s.model.Close()
		s.model = nil
	}
}

// nodeText 将符号转成 embedding 输入文本（Name + Signature + Doc，方法带接收者）。
func nodeText(n *codegraph.Node) string {
	var b strings.Builder
	if n.Receiver != "" {
		b.WriteString(n.Receiver)
		b.WriteByte('.')
	}
	b.WriteString(n.Name)
	if n.Signature != "" {
		b.WriteByte(' ')
		b.WriteString(n.Signature)
	}
	if n.Doc != "" {
		b.WriteByte(' ')
		b.WriteString(n.Doc)
	}
	return b.String()
}

// vecs 为全部符号生成向量（node ID → 向量）与维度。
func (s *Service) vecs(nodes []codegraph.Node) (map[int][]float32, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.model == nil {
		return nil, 0, fmt.Errorf("semantic: 模型未加载")
	}
	vecs := make(map[int][]float32, len(nodes))
	for i := range nodes {
		n := &nodes[i]
		v, err := s.model.Embed(nodeText(n))
		if err != nil {
			return nil, 0, fmt.Errorf("semantic: 符号 %s 向量化失败: %v", n.Name, err)
		}
		vecs[n.ID] = v
	}
	return vecs, s.model.Dim(), nil
}

// VecBuilder 返回可供 codegraph.Store 注册的全量向量化回调：
// 每次 Reindex 构建索引后调用，随索引持久化（node ID → 向量）。
func (s *Service) VecBuilder() func(nodes []codegraph.Node) (map[int][]float32, int, error) {
	return s.vecs
}

// Index 为 ix 的全部符号生成向量并填充 ix.Vecs / ix.VecDim（原地修改）。
// 任一符号向量化失败即整体失败并保持原 Vecs 不变（调用方回退词法）。
func (s *Service) Index(ix *codegraph.Index) error {
	vecs, dim, err := s.vecs(ix.Nodes)
	if err != nil {
		return err
	}
	ix.Vecs = vecs
	ix.VecDim = dim
	return nil
}

// HasVec 报告索引是否带符号向量（语义检索可用）。
func HasVec(ix *codegraph.Index) bool { return ix != nil && len(ix.Vecs) > 0 }

// Rerank 对 FTS5 检索出的候选节点做语义重排：查询向量化后与候选向量做余弦
// 相似度，整串精确命中（整名/限定名/文件名）置顶，其余按相似度降序（低于阈值 0.25 剔除）。
// ids 为空 / Vecs 缺失 / 查询向量化失败时，按 FTS5 原始顺序构造 Match 回退。
func (s *Service) Rerank(ix *codegraph.Index, root, query string, ids []int, maxResults int) []codegraph.Match {
	if ix == nil || len(ids) == 0 {
		return nil
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}
	if !HasVec(ix) {
		return matchesOf(ix, root, ids, maxResults)
	}
	s.mu.Lock()
	qv, err := s.model.Embed(q)
	s.mu.Unlock()
	if err != nil {
		return matchesOf(ix, root, ids, maxResults) // 查询向量化失败：按 FTS5 顺序回退
	}

	lq := strings.ToLower(q)
	type cand struct {
		id  int
		sim float32
	}
	var exact []cand // 整串精确命中
	var fuzzy []cand // 语义候选（余弦 ≥ 阈值）
	for _, id := range ids {
		if id < 0 || id >= len(ix.Nodes) {
			continue
		}
		vec, ok := ix.Vecs[id]
		if !ok {
			continue
		}
		sim := embed.Cosine(qv, vec)
		if exactScore(&ix.Nodes[id], lq) > 0 {
			exact = append(exact, cand{id, sim})
			continue
		}
		if sim >= 0.25 {
			fuzzy = append(fuzzy, cand{id, sim})
		}
	}
	sort.Slice(exact, func(a, b int) bool {
		ea, eb := exactScore(&ix.Nodes[exact[a].id], lq), exactScore(&ix.Nodes[exact[b].id], lq)
		if ea != eb {
			return ea > eb
		}
		return exact[a].id < exact[b].id
	})
	sort.Slice(fuzzy, func(a, b int) bool { return fuzzy[a].sim > fuzzy[b].sim })

	out := make([]codegraph.Match, 0, len(exact)+len(fuzzy))
	for _, c := range exact {
		m := ix.MatchByID(root, c.id)
		m.Semantic = c.sim
		out = append(out, m)
	}
	for _, c := range fuzzy {
		m := ix.MatchByID(root, c.id)
		m.Semantic = c.sim
		out = append(out, m)
	}
	if len(out) > maxResults {
		out = out[:maxResults]
	}
	return out
}

// matchesOf 按给定顺序构造 Match（FTS5 rank 顺序回退）。
func matchesOf(ix *codegraph.Index, root string, ids []int, maxResults int) []codegraph.Match {
	out := make([]codegraph.Match, 0, len(ids))
	for _, id := range ids {
		if id < 0 || id >= len(ix.Nodes) {
			continue
		}
		out = append(out, ix.MatchByID(root, id))
		if len(out) >= maxResults {
			break
		}
	}
	return out
}

// exactScore 整串精确命中打分：整名 100 > 限定名 95 > 文件名 90；非整串命中返回 0。
func exactScore(n *codegraph.Node, lq string) int {
	if n.Name != "" && strings.ToLower(n.Name) == lq {
		return 100
	}
	if n.Receiver != "" && strings.ToLower(n.Receiver)+"."+strings.ToLower(n.Name) == lq {
		return 95
	}
	if n.File != "" && strings.ToLower(n.File) == lq {
		return 90
	}
	return 0
}
