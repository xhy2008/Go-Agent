package tools

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestWriteAndReadFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "a.txt")

	w := &WriteFileTool{}
	_, err := w.Execute(context.Background(), map[string]any{
		"path":    p,
		"content": "line1\nline2\nline3\n",
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	r := &ReadFileRangeTool{}
	got, err := r.Execute(context.Background(), map[string]any{
		"path":       p,
		"start_line": float64(1),
		"end_line":   float64(2),
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(got, "line1") || !strings.Contains(got, "line2") || strings.Contains(got, "line3") {
		t.Fatalf("read range mismatch:\n%s", got)
	}

	// 追加
	_, err = w.Execute(context.Background(), map[string]any{
		"path":    p,
		"content": "line4\n",
		"append":  true,
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "line1\nline2\nline3\nline4\n" {
		t.Fatalf("append result mismatch: %q", string(data))
	}
}

func TestSafePathBlocksSystem(t *testing.T) {
	for _, p := range []string{`C:\Windows\System32`, `/etc/passwd`, `/usr/bin`} {
		if _, err := safePath(p); err == nil {
			t.Errorf("expected block for %s", p)
		}
	}
	if _, err := safePath(""); err == nil {
		t.Error("expected error for empty path")
	}
	// 用户项目中的同名目录不应误伤
	for _, p := range []string{`E:\myproj\bin\app.exe`, `E:\myproj\var\data.txt`, `E:\myproj\build\out`} {
		if _, err := safePath(p); err != nil {
			t.Errorf("unexpected block for %s: %v", p, err)
		}
	}
}

func TestSearchFileNames(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "main.go"), "package main\n")
	mustWrite(t, filepath.Join(dir, "main_test.go"), "package main\n")
	mustWrite(t, filepath.Join(dir, "app.txt"), "x\n")

	s := &SearchFileNamesTool{}
	got, err := s.Execute(context.Background(), map[string]any{
		"path":  dir,
		"regex": `\.go$`,
	})
	if err != nil {
		t.Fatalf("search names: %v", err)
	}
	if !strings.Contains(got, "main.go") || !strings.Contains(got, "main_test.go") {
		t.Fatalf("missing go files:\n%s", got)
	}
	if strings.Contains(got, "app.txt") {
		t.Fatalf("txt should not match:\n%s", got)
	}

	// 无效正则报错
	if _, err := s.Execute(context.Background(), map[string]any{"path": dir, "regex": "["}); err == nil {
		t.Error("expected error for invalid regex")
	}
}

func TestSearchFileContent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.go")
	mustWrite(t, p, "line1 foo\nline2 bar\nline3 foo\n")

	s := &SearchFileContentTool{}
	got, err := s.Execute(context.Background(), map[string]any{"path": p, "regex": "foo"})
	if err != nil {
		t.Fatalf("search content: %v", err)
	}
	if !strings.Contains(got, "1\t") || !strings.Contains(got, "3\t") || strings.Contains(got, "2\t") {
		t.Fatalf("content match mismatch:\n%s", got)
	}

	// 目录应报错
	if _, err := s.Execute(context.Background(), map[string]any{"path": t.TempDir(), "regex": "foo"}); err == nil {
		t.Error("expected error for directory path")
	}
}

func TestEditFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.go")
	mustWrite(t, p, "func a() { return 1 }\nfunc b() { return 1 }\n")

	e := &EditFileTool{}
	got, err := e.Execute(context.Background(), map[string]any{
		"path":        p,
		"old_content": "return 1",
		"new_content": "return 2",
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if !strings.Contains(got, "2 处") {
		t.Fatalf("expected 2 replacements, got: %s", got)
	}
	data, _ := os.ReadFile(p)
	if strings.Contains(string(data), "return 1") || !strings.Contains(string(data), "return 2") {
		t.Fatalf("edit result mismatch: %q", string(data))
	}

	// 未找到原内容应报错
	if _, err := e.Execute(context.Background(), map[string]any{
		"path": p, "old_content": "not-exist", "new_content": "x",
	}); err == nil {
		t.Error("expected error when old_content not found")
	}
}

func TestExecCommandBlacklist(t *testing.T) {
	cmd := &ExecCommandTool{}
	for _, dangerous := range []string{"rm -rf /", "format c:", "shutdown /s"} {
		_, err := cmd.Execute(context.Background(), map[string]any{"command": dangerous})
		if err == nil || !strings.Contains(err.Error(), "拦截") {
			t.Errorf("expected blacklist block for %q, got %v", dangerous, err)
		}
	}
}

func TestBackgroundJob(t *testing.T) {
	cmd := &ExecCommandTool{}
	got, err := cmd.Execute(context.Background(), map[string]any{
		"command":    "echo hello",
		"background": true,
	})
	if err != nil {
		t.Fatalf("start bg: %v", err)
	}
	id := regexp.MustCompile(`bg\d+`).FindString(got)
	if id == "" {
		t.Fatalf("no task id in response: %s", got)
	}

	checker := &CheckCommandStatusTool{}
	deadline := time.Now().Add(5 * time.Second)
	status := ""
	for time.Now().Before(deadline) {
		status, err = checker.Execute(context.Background(), map[string]any{"task_id": id})
		if err != nil {
			t.Fatalf("check status: %v", err)
		}
		if strings.Contains(status, "已完成") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(status, "已完成") || !strings.Contains(status, "hello") {
		t.Fatalf("bad bg status:\n%s", status)
	}

	// 不存在的任务报错
	if _, err := checker.Execute(context.Background(), map[string]any{"task_id": "bg9999"}); err == nil {
		t.Error("expected error for unknown task")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
