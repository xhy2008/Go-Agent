package memory

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestLongTermSearch(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent.db")
	m, err := OpenLongTerm(p)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	_ = m.Add("用户: 用 Go 实现了一个 HTTP 服务器，使用 net/http")
	_ = m.Add("用户: 配置了 DeepSeek API 用于对话")
	_ = m.Add("用户: 编写了单元测试覆盖工具函数")

	ctx := context.Background()
	hits, err := m.Search(ctx, "Go HTTP 服务器", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("expected search hits")
	}
	if !strings.Contains(hits[0], "HTTP") {
		t.Fatalf("first hit should be most relevant, got: %s", hits[0])
	}

	// 不相关查询应返回空
	hits, err = m.Search(ctx, "xyzabc123不存在", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected no hits, got %v", hits)
	}

	// 重新打开后条目仍在
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	m2, err := OpenLongTerm(p)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	if m2.Count() != 3 {
		t.Fatalf("expected 3 entries after reopen, got %d", m2.Count())
	}
}
