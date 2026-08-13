package codegraph

import (
	"path/filepath"
	"runtime"
	"testing"
)

// repoRoot 返回本项目根目录（codegraph 包位于 <root>/internal/codegraph）。
func repoRoot(tb testing.TB) string {
	tb.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		tb.Fatal("无法定位源码路径")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// TestSmoke_RepoIndex 在真实项目根目录上做端到端冒烟：
// 全量构建 → 增量快路径 → 查询带源码/调用关系。
func TestSmoke_RepoIndex(t *testing.T) {
	root := repoRoot(t)
	st := NewStore()
	t.Cleanup(st.Close)

	ix1, err := st.Reindex(root)
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	if len(ix1.Nodes) < 100 {
		t.Errorf("本项目符号数=%d, 期望 ≥100（解析不完整？）", len(ix1.Nodes))
	}
	t.Logf("全量索引: %d 文件 / %d 符号 / %d 关系", len(ix1.FileFp), len(ix1.Nodes), len(ix1.Edges))

	// 无变更：增量快路径应返回同一索引
	ix2, err := st.Reindex(root)
	if err != nil {
		t.Fatalf("Reindex(快路径): %v", err)
	}
	if ix2 != ix1 {
		t.Error("无变更时应命中增量快路径")
	}

	// 查询真实符号：Agent.Run（func）
	ms := ix1.Explore(root, "Run", 5)
	if len(ms) == 0 || ms[0].Node.Name != "Run" {
		t.Fatalf("查询 Run 首条=%+v", ms[0].Node)
	}
	if len(ms[0].Source) == 0 {
		t.Error("Run 应返回源码行")
	}

	// 查询 readStream（llm 内部函数，文件名/前缀命中）
	ms2 := ix1.Explore(root, "readStream", 5)
	if len(ms2) == 0 {
		t.Fatal("查询 readStream 未命中")
	}

	// 查询真实方法：Agent.SetSkills
	ms3 := ix1.Explore(root, "Agent.SetSkills", 5)
	if len(ms3) == 0 || ms3[0].Node.Receiver != "Agent" || ms3[0].Node.Name != "SetSkills" {
		t.Fatalf("查询 Agent.SetSkills 首条=%+v", ms3[0].Node)
	}

	// ---- 词法建边验证（真实项目，取唯一符号）----
	// Store.Reindex 同包调用 Store.finish（词法可解：唯一同名方法）
	reidx := byNameRecvUnique(t, ix1, "Reindex", "Store")
	finish := byNameRecvUnique(t, ix1, "finish", "Store")
	if !edgeExists(ix1, reidx, finish, EdgeCall) {
		t.Error("缺少词法边：Store.Reindex → Store.finish（同包方法调用）")
	}
}

// byNameUnique 按名字取唯一节点 ID。
func byNameUnique(t *testing.T, ix *Index, name string) int {
	t.Helper()
	ids := ix.ByName[name]
	if len(ids) != 1 {
		t.Fatalf("符号 %q 节点数=%d（IDs=%v）", name, len(ids), ids)
	}
	return ids[0]
}

// byNameRecvUnique 按方法名+接收者取唯一节点 ID。
func byNameRecvUnique(t *testing.T, ix *Index, name, recv string) int {
	t.Helper()
	ids := ix.ByName[name]
	for _, id := range ids {
		if ix.Nodes[id].Receiver == recv {
			return id
		}
	}
	t.Fatalf("方法 %s.%s 未找到", recv, name)
	return -1
}

// BenchmarkRepoReindex 全量构建耗时（真实项目根）。
func BenchmarkRepoReindex(b *testing.B) {
	root := repoRoot(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st := NewStore()
		if _, err := st.Reindex(root); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRepoIncremental 增量（无变更快路径）耗时。
func BenchmarkRepoIncremental(b *testing.B) {
	root := repoRoot(b)
	st := NewStore()
	if _, err := st.Reindex(root); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := st.Reindex(root); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRepoExplore 查询延迟。
func BenchmarkRepoExplore(b *testing.B) {
	root := repoRoot(b)
	st := NewStore()
	ix, err := st.Reindex(root)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ix.Explore(root, "Run", 3)
	}
}
