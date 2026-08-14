package codegraph

import (
	"os"
	"path/filepath"
	"testing"
)

// setupStore 将 golden 文件复制到临时目录并构建一次索引。
func setupStore(t *testing.T) (string, *Store) {
	t.Helper()
	root := t.TempDir()
	for _, f := range []string{"main.go", "helper.go"} {
		data, err := os.ReadFile(filepath.Join("testdata", "golden", f))
		if err != nil {
			t.Fatalf("读取 golden %s: %v", f, err)
		}
		if err := os.WriteFile(filepath.Join(root, f), data, 0o644); err != nil {
			t.Fatalf("写入 %s: %v", f, err)
		}
	}
	st := NewStore()
	t.Cleanup(st.Close)
	if _, err := st.Reindex(root); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	return root, st
}

func TestStore_Reindex(t *testing.T) {
	root, st := setupStore(t)

	ix := st.Index()
	if ix == nil {
		t.Fatal("索引未构建")
	}
	if len(ix.Nodes) != 16 {
		t.Errorf("节点数=%d, 期望 16", len(ix.Nodes))
	}
	if len(ix.FileFp) != 2 {
		t.Errorf("FileFp=%v, 期望 2 个文件", ix.FileFp)
	}
	// 索引已落盘
	if _, err := os.Stat(filepath.Join(root, IndexDirName, IndexFileName)); err != nil {
		t.Fatalf("索引文件未落盘: %v", err)
	}
}

func TestStore_Incremental(t *testing.T) {
	root, st := setupStore(t)
	ix1 := st.Index()

	// 无变更：快路径，返回同一索引指针
	ix2, err := st.Reindex(root)
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	if ix2 != ix1 {
		t.Error("无变更时应返回同一索引（快路径）")
	}

	// 修改 main.go → 必须重建
	path := filepath.Join(root, "main.go")
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(data, []byte("\n// changed\n")...), 0o644); err != nil {
		t.Fatalf("修改文件: %v", err)
	}
	ix3, err := st.Reindex(root)
	if err != nil {
		t.Fatalf("Reindex(变更): %v", err)
	}
	if ix3 == ix1 {
		t.Error("文件变更后应重建索引")
	}
	if len(ix3.Nodes) != 16 {
		t.Errorf("重建后节点数=%d, 期望 16", len(ix3.Nodes))
	}
	// 重建后查询反映新状态（指纹更新）
	if ix3.FileFp["main.go"] == ix1.FileFp["main.go"] {
		t.Error("main.go 指纹应更新")
	}
	if ix3.FileFp["helper.go"] != ix1.FileFp["helper.go"] {
		t.Error("helper.go 未变更，指纹不应变化")
	}
}

func TestStore_DeleteFile(t *testing.T) {
	root, st := setupStore(t)

	if err := os.Remove(filepath.Join(root, "helper.go")); err != nil {
		t.Fatalf("删除文件: %v", err)
	}
	ix2, err := st.Reindex(root)
	if err != nil {
		t.Fatalf("Reindex(删除): %v", err)
	}
	if _, ok := ix2.FileFp["helper.go"]; ok {
		t.Error("删除的文件不应再出现在 FileFp")
	}
	if len(ix2.Nodes) != 8 {
		t.Errorf("删除后节点数=%d, 期望 8（仅 main.go）", len(ix2.Nodes))
	}
}

func TestLoadStore(t *testing.T) {
	root, st := setupStore(t)
	st.Close() // 先关闭构建期连接，再验证独立 LoadStore（Windows 文件锁）

	st2 := LoadStore(root)
	t.Cleanup(st2.Close)
	ix := st2.Index()
	if ix == nil {
		t.Fatal("LoadStore 未加载到索引")
	}
	ms := ix.Explore(root, "add", 5)
	if len(ms) == 0 || ms[0].Node.Name != "add" {
		t.Fatalf("加载后查询失败: %+v", ms)
	}
}

func TestStore_VecsRoundtrip(t *testing.T) {
	// VecBuilder 回调在落盘前填充 Vecs，保存/加载往返后向量不应丢失（语义持久化）。
	root := t.TempDir()
	for _, f := range []string{"main.go", "helper.go"} {
		data, err := os.ReadFile(filepath.Join("testdata", "golden", f))
		if err != nil {
			t.Fatalf("读取 golden %s: %v", f, err)
		}
		if err := os.WriteFile(filepath.Join(root, f), data, 0o644); err != nil {
			t.Fatalf("写入 %s: %v", f, err)
		}
	}
	st := NewStore()
	t.Cleanup(st.Close)
	st.VecBuilder = func(nodes []Node) (map[int][]float32, int, error) {
		vecs := make(map[int][]float32, len(nodes))
		for _, n := range nodes {
			vecs[n.ID] = []float32{float32(n.ID), float32(len(n.Name))}
		}
		return vecs, 2, nil
	}
	ix, err := st.Reindex(root)
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	if ix.VecDim != 2 || len(ix.Vecs) != len(ix.Nodes) {
		t.Fatalf("Vecs 未填充: dim=%d n=%d nodes=%d", ix.VecDim, len(ix.Vecs), len(ix.Nodes))
	}
	// 落盘后重新加载，向量与维度应保留
	st.Close() // 先关闭构建期连接，避免 Windows 文件锁
	st2 := LoadStore(root)
	t.Cleanup(st2.Close)
	ix2 := st2.Index()
	if ix2 == nil || ix2.VecDim != 2 || len(ix2.Vecs) != len(ix2.Nodes) {
		t.Fatalf("加载后 Vecs 丢失: dim=%d n=%d nodes=%d", ix2.VecDim, len(ix2.Vecs), len(ix2.Nodes))
	}
}

func TestGoFiles_SkipDirs(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".go-agent"), 0o755)
	os.MkdirAll(filepath.Join(root, "build"), 0o755)
	os.MkdirAll(filepath.Join(root, "vendor"), 0o755)
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package x\n"), 0o644)
	os.WriteFile(filepath.Join(root, ".go-agent", "x.go"), []byte("package x\n"), 0o644)
	os.WriteFile(filepath.Join(root, "build", "b.go"), []byte("package x\n"), 0o644)
	os.WriteFile(filepath.Join(root, "vendor", "c.go"), []byte("package x\n"), 0o644)

	files, err := goFiles(root)
	if err != nil {
		t.Fatalf("goFiles: %v", err)
	}
	if len(files) != 1 || files[0] != "a.go" {
		t.Errorf("goFiles=%v, 期望仅 [a.go]", files)
	}
}
