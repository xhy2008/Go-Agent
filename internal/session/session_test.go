package session

import (
	"path/filepath"
	"strings"
	"testing"

	"go-agent/internal/llm"
)

func TestSaveLoadList(t *testing.T) {
	dir := t.TempDir()
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "你好"},
		{Role: "assistant", Content: "你好！"},
	}

	p, err := Save(dir, "test1", msgs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p, "test1.json") {
		t.Fatalf("unexpected path: %s", p)
	}

	loaded, err := Load(dir, "test1")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 3 || loaded[1].Content != "你好" {
		t.Fatalf("load mismatch: %+v", loaded)
	}

	names, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "test1.json" {
		t.Fatalf("list mismatch: %v", names)
	}

	// 不存在的目录返回空列表
	names, err = List(filepath.Join(t.TempDir(), "nope"))
	if err != nil || names != nil {
		t.Fatalf("expected empty list, got %v %v", names, err)
	}
}
