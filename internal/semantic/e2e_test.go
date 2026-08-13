package semantic

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go-agent/internal/codegraph"
	"go-agent/internal/embed"
)

// TestE2E_RealRepo 在真实仓库（本模块）上端到端验证：
// 全量符号向量化 → 语义查询命中与排序。
func TestE2E_RealRepo(t *testing.T) {
	s, err := Load(modelPath(t), embed.PoolLast)
	if err != nil {
		t.Skipf("embedding 运行时不可用（缺少 llama_bridge.dll?），跳过: %v", err)
	}
	defer s.Close()

	root := repoRoot()
	var rels []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == "build" || name == "vendor" || name == "node_modules" || name == ".git" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".go") {
			rel, _ := filepath.Rel(root, path)
			rels = append(rels, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) < 50 {
		t.Fatalf("源码文件数=%d, 期望 >=50", len(rels))
	}
	var fgs []*codegraph.FileGraph
	for _, rel := range rels {
		g, err := codegraph.ParseFile(filepath.Join(root, filepath.FromSlash(rel)), rel)
		if err != nil {
			continue
		}
		fgs = append(fgs, g)
	}
	ix, err := codegraph.Build(root, "go-agent", fgs)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := s.Index(ix); err != nil {
		t.Fatalf("Index: %v", err)
	}
	t.Logf("索引: %d 符号 / 向量维度 %d", len(ix.Nodes), ix.VecDim)

	// 候选列表：模拟 FTS5 召回全部节点（e2e 侧重语义排序本身）
	allIDs := make([]int, 0, len(ix.Nodes))
	for i := range ix.Nodes {
		allIDs = append(allIDs, ix.Nodes[i].ID)
	}

	// 语义查询 1：模糊描述应命中核心函数
	ms := s.Rerank(ix, root, "parse command line arguments and run the agent loop", allIDs, 5)
	if len(ms) == 0 {
		t.Fatal("语义查询 1 无命中")
	}
	found := false
	for i := range ms {
		if strings.Contains(ms[i].Node.Name, "main") || strings.Contains(ms[i].Node.Name, "Run") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("语义查询 1 应命中 main/Run 类符号，实际 %v", ms)
	}

	// 语义查询 2：模糊描述应命中代码图相关符号
	ms2 := s.Rerank(ix, root, "find where a function is called in the code graph", allIDs, 5)
	if len(ms2) == 0 {
		t.Fatal("语义查询 2 无命中")
	}
	graphFound := false
	for i := range ms2 {
		n := ms2[i].Node.Name
		if strings.Contains(strings.ToLower(n), "explore") || strings.Contains(strings.ToLower(n), "ref") || strings.Contains(strings.ToLower(n), "call") {
			graphFound = true
			break
		}
	}
	if !graphFound {
		t.Errorf("语义查询 2 应命中 explore/ref 类符号，实际 %v", ms2)
	}

	// 精确限定名仍应命中词法优先
	ms3 := s.Rerank(ix, root, "Agent.SetSkills", allIDs, 3)
	if len(ms3) == 0 || ms3[0].Node.Name != "SetSkills" || ms3[0].Node.Receiver != "Agent" {
		t.Errorf("精确限定名查询首条=%+v", ms3)
	}
}

// repoRoot 返回本模块根目录（semantic 包位于 <root>/internal/semantic）。
func repoRoot() string {
	dir, _ := os.Getwd()
	// 当前工作目录为 internal/semantic（go test 运行目录）
	return filepath.Dir(filepath.Dir(filepath.Dir(dir)))
}
