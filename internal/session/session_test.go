package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go-agent/internal/llm"
)

func TestSaveLoadList(t *testing.T) {
	dir := t.TempDir()
	rec := Record{
		WorkingDir: `D:\work\proj`,
		Messages: []llm.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "你好"},
			{Role: "assistant", Content: "你好！"},
		},
	}

	p, err := Save(dir, "test1", rec)
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
	if loaded.WorkingDir != rec.WorkingDir {
		t.Fatalf("working dir mismatch: %q", loaded.WorkingDir)
	}
	if len(loaded.Messages) != 3 || loaded.Messages[1].Content != "你好" {
		t.Fatalf("load mismatch: %+v", loaded.Messages)
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

// TestLoadLegacyArray 兼容旧格式：会话文件是裸消息数组（旧版仅存消息），工作目录为空。
func TestLoadLegacyArray(t *testing.T) {
	dir := t.TempDir()
	legacy := `[
  {"role": "user", "content": "旧消息"},
  {"role": "assistant", "content": "旧回复"}
]`
	if err := os.WriteFile(filepath.Join(dir, "old.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, err := Load(dir, "old")
	if err != nil {
		t.Fatal(err)
	}
	if rec.WorkingDir != "" {
		t.Fatalf("legacy 文件工作目录应为空，got %q", rec.WorkingDir)
	}
	if len(rec.Messages) != 2 || rec.Messages[0].Content != "旧消息" {
		t.Fatalf("legacy 消息解析失败: %+v", rec.Messages)
	}
}

// TestSaveTimestampName name 为空且无工作目录时生成时间戳文件名。
func TestSaveTimestampName(t *testing.T) {
	dir := t.TempDir()
	p, err := Save(dir, "", Record{Messages: []llm.Message{{Role: "user", Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Base(p)
	if !strings.HasPrefix(base, "20") || !strings.HasSuffix(base, ".json") {
		t.Fatalf("unexpected timestamp name: %s", base)
	}
}

// TestNameForWorkingDir 会话名优先用工作目录末级目录名；同名冲突时加序号。
func TestNameForWorkingDir(t *testing.T) {
	dir := t.TempDir()
	rec := Record{WorkingDir: `D:\work\proj`, Messages: []llm.Message{{Role: "user", Content: "x"}}}

	p1, err := Save(dir, "", rec)
	if err != nil {
		t.Fatal(err)
	}
	if base := filepath.Base(p1); base != "proj.json" {
		t.Fatalf("first name should be proj.json, got %s", base)
	}
	p2, err := Save(dir, "", rec)
	if err != nil {
		t.Fatal(err)
	}
	if base := filepath.Base(p2); base != "proj-2.json" {
		t.Fatalf("second name should be proj-2.json, got %s", base)
	}
	// 带尾部分隔符的路径同样取末级目录名。
	n := NameFor(dir, Record{WorkingDir: `D:\work\proj\`})
	if n != "proj-3" {
		t.Fatalf("trailing sep name should be proj-3, got %s", n)
	}
}
