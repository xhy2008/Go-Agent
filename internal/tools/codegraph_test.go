package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"go-agent/internal/codegraph"
)

func buildGoldenIndex(t *testing.T) (*codegraph.Index, string) {
	t.Helper()
	root := filepath.Join("..", "codegraph", "testdata", "golden")
	var fgs []*codegraph.FileGraph
	for _, f := range []string{"helper.go", "main.go"} {
		fg, err := codegraph.ParseFile(filepath.Join(root, f), f)
		if err != nil {
			t.Fatalf("ParseFile(%s): %v", f, err)
		}
		fgs = append(fgs, fg)
	}
	ix, err := codegraph.Build("", "", fgs)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return ix, root
}

// newToolSet 构造使用内存词法 Explore 的工具集（无 FTS/无语义）。
func newToolSet(t *testing.T) *CodegraphToolSet {
	t.Helper()
	ix, root := buildGoldenIndex(t)
	return &CodegraphToolSet{
		Index: func() *codegraph.Index { return ix },
		Root:  root,
	}
}

func TestCodegraphSearchTool_Query(t *testing.T) {
	tool := &CodegraphSearchTool{newToolSet(t)}
	out, err := tool.Execute(context.Background(), map[string]any{"query": "add"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"### 1. add", "签名: (a, b int) int", "索引构建时间:"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q\n---\n%s", want, out)
		}
	}
}

func TestCodegraphNodeTool_Details(t *testing.T) {
	tool := &CodegraphNodeTool{newToolSet(t)}
	out, err := tool.Execute(context.Background(), map[string]any{"symbol": "add"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"### 1. add", "签名: (a, b int) int", "```go", "被调用于:"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q\n---\n%s", want, out)
		}
	}
	// helper 应出现在调用者中
	if !strings.Contains(out, "helper") {
		t.Errorf("add 的调用者应包含 helper，实际输出:\n%s", out)
	}
}

func TestCodegraphCallersTool(t *testing.T) {
	tool := &CodegraphCallersTool{newToolSet(t)}
	out, err := tool.Execute(context.Background(), map[string]any{"symbol": "add"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "调用者链") || !strings.Contains(out, "helper") {
		t.Errorf("add 的调用者链应包含 helper，实际输出:\n%s", out)
	}
}

func TestCodegraphTraceTool(t *testing.T) {
	tool := &CodegraphTraceTool{newToolSet(t)}
	out, err := tool.Execute(context.Background(), map[string]any{"symbol": "add"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "调用链") || !strings.Contains(out, "helper") {
		t.Errorf("add 的调用链应包含 helper，实际输出:\n%s", out)
	}
}

func TestCodegraphImpactTool(t *testing.T) {
	tool := &CodegraphImpactTool{newToolSet(t)}
	out, err := tool.Execute(context.Background(), map[string]any{"symbol": "add"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "影响面") {
		t.Errorf("输出应包含影响面标题:\n%s", out)
	}
}

func TestCodegraphToolSet_NotInitialized(t *testing.T) {
	tool := &CodegraphSearchTool{&CodegraphToolSet{Root: "."}}
	out, err := tool.Execute(context.Background(), map[string]any{"query": "x"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "未初始化") {
		t.Errorf("索引未初始化时应返回提示，实际: %s", out)
	}
}

func TestCodegraphToolSet_NoHit(t *testing.T) {
	tool := &CodegraphSearchTool{newToolSet(t)}
	out, err := tool.Execute(context.Background(), map[string]any{"query": "zzz_not_exist"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "未找到") {
		t.Errorf("未命中时应返回提示，实际: %s", out)
	}
}

func TestCodegraphToolSet_EmptyQuery(t *testing.T) {
	tool := &CodegraphSearchTool{&CodegraphToolSet{Root: "."}}
	if _, err := tool.Execute(context.Background(), map[string]any{"query": "  "}); err == nil {
		t.Error("空 query 应报错")
	}
}

func TestCodegraphToolSet_EmptySymbol(t *testing.T) {
	tool := &CodegraphNodeTool{&CodegraphToolSet{Root: "."}}
	if _, err := tool.Execute(context.Background(), map[string]any{"symbol": "  "}); err == nil {
		t.Error("空 symbol 应报错")
	}
}
