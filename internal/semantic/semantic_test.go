package semantic

import (
	"os"
	"path/filepath"
	"testing"

	"go-agent/internal/codegraph"
	"go-agent/internal/embed"
)

// modelPath 返回 nomic-embed-text-v1.5 模型路径（不存在则测试跳过）。
func modelPath(t *testing.T) string {
	t.Helper()
	paths := []string{
		filepath.Join("..", "..", "models", "nomic-embed-text-v1.5.Q8_0.gguf"),
		filepath.Join("..", "..", "..", "models", "nomic-embed-text-v1.5.Q8_0.gguf"),
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			abs, _ := filepath.Abs(p)
			return abs
		}
	}
	t.Skip("未找到 nomic-embed-text-v1.5.Q8_0.gguf，跳过语义检索测试")
	return ""
}

func TestEmbedBasic(t *testing.T) {
	m, err := embed.Load(modelPath(t), embed.PoolLast)
	if err != nil {
		t.Skipf("embedding 运行时不可用（缺少 llama_bridge.dll?），跳过: %v", err)
	}
	defer m.Close()
	if m.Dim() != 768 {
		t.Fatalf("维度=%d, 期望 768", m.Dim())
	}
	a, err := m.Embed("Greet returns a greeting message")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	b, err := m.Embed("Greet returns a greeting message")
	if err != nil {
		t.Fatalf("Embed2: %v", err)
	}
	c, err := m.Embed("initialize the database connection pool")
	if err != nil {
		t.Fatalf("Embed3: %v", err)
	}
	if sim := embed.Cosine(a, b); sim < 0.99 {
		t.Errorf("相同文本余弦=%v, 期望 ≈1", sim)
	}
	if simAC := embed.Cosine(a, c); simAC >= 0.9 {
		t.Errorf("不同主题余弦=%v, 期望显著低于 0.9", simAC)
	}
	t.Logf("同文相似度=%.4f, 异题相似度=%.4f", embed.Cosine(a, b), embed.Cosine(a, c))
}

func TestServiceIndexAndExplore(t *testing.T) {
	s, err := Load(modelPath(t), embed.PoolLast)
	if err != nil {
		t.Skipf("embedding 运行时不可用（缺少 llama_bridge.dll?），跳过: %v", err)
	}
	defer s.Close()

	// 用 golden 夹具构建一个小索引
	root := filepath.Join("..", "codegraph", "testdata", "golden")
	dir := t.TempDir()
	var rels []string
	for _, f := range []string{"main.go", "helper.go"} {
		data, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), data, 0o644); err != nil {
			t.Fatal(err)
		}
		rels = append(rels, f)
	}
	st := codegraph.NewStore()
	t.Cleanup(st.Close)
	ix, err := st.Reindex(dir)
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	if err := s.Index(ix); err != nil {
		t.Fatalf("Index: %v", err)
	}
	if ix.VecDim != 768 || len(ix.Vecs) != len(ix.Nodes) {
		t.Fatalf("Vecs 异常: dim=%d n=%d nodes=%d", ix.VecDim, len(ix.Vecs), len(ix.Nodes))
	}

	// 语义查询：模糊语义词应命中问候相关符号（Greet 方法或 Hello 类型）
	// Rerank 输入为候选 ID 列表（此处模拟 FTS5 召回全部节点）
	allIDs := make([]int, 0, len(ix.Nodes))
	for i := range ix.Nodes {
		allIDs = append(allIDs, ix.Nodes[i].ID)
	}
	ms := s.Rerank(ix, dir, "say hello to someone", allIDs, 5)
	if len(ms) == 0 {
		t.Fatal("语义查询无命中")
	}
	greetFound := false
	for i := range ms {
		if ms[i].Node.Name == "Greet" || ms[i].Node.Name == "Hello" {
			greetFound = true
			break
		}
	}
	if !greetFound {
		t.Errorf("语义查询应命中问候相关符号，实际 %v", ms)
	}
	// 精确命中保持优先：Hello.Greet 应排在最前
	ms2 := s.Rerank(ix, dir, "Hello.Greet", allIDs, 5)
	if len(ms2) == 0 || ms2[0].Node.Name != "Greet" || ms2[0].Node.Receiver != "Hello" {
		t.Errorf("精确限定名查询首条=%+v", ms2)
	}
	// 未启用向量时回退 FTS5 顺序（词法由 codegraph 层负责）
	ix.Vecs = nil
	if len(s.Rerank(ix, dir, "add", allIDs, 5)) == 0 {
		t.Error("Vecs 为空时应按候选顺序返回")
	}
}
