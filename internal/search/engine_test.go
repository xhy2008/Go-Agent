package search

import (
	"context"
	"errors"
	"testing"
)

// fakeEngine 用于测试的假搜索后端。
type fakeEngine struct {
	name    string
	err     error
	results []Result
}

func (f *fakeEngine) Name() string { return f.name }
func (f *fakeEngine) Search(ctx context.Context, query string, maxResults int) ([]Result, error) {
	return f.results, f.err
}

func TestManagerSearchFallback(t *testing.T) {
	m := &Manager{
		Primary:  &fakeEngine{name: "brave", err: errors.New("connection refused")},
		Fallback: &fakeEngine{name: "ddg", results: []Result{{Title: "x", URL: "https://x"}}},
	}
	// 主后端失败时应降级到 fallback 并返回其结果（降级警告打到 stderr）
	results, err := m.Search(context.Background(), "q", 5)
	if err != nil {
		t.Fatalf("fallback should succeed, got %v", err)
	}
	if len(results) != 1 || results[0].URL != "https://x" {
		t.Fatalf("unexpected fallback results: %+v", results)
	}

	// 主后端与 fallback 相同（仅 ddg）时，失败直接返回错误
	m2 := &Manager{
		Primary:  &fakeEngine{name: "ddg", err: errors.New("boom")},
		Fallback: nil,
	}
	if _, err := m2.Search(context.Background(), "q", 5); err == nil {
		t.Fatal("expected error when no fallback available")
	}
}

func TestNewManagerFallback(t *testing.T) {
	// 无 Brave key / 无 SearXNG URL / 未知后端 / 空后端：主后端未配置（nil）
	if m := NewManager("brave", "", ""); m.Primary != nil {
		t.Errorf("brave without key should have nil primary, got %s", m.Primary.Name())
	}
	if m := NewManager("searxng", "", ""); m.Primary != nil {
		t.Errorf("searxng without url should have nil primary, got %s", m.Primary.Name())
	}
	if m := NewManager("ddg", "", ""); m.Primary != nil {
		t.Errorf("unknown backend should have nil primary, got %s", m.Primary.Name())
	}
	if m := NewManager("", "", ""); m.Primary != nil {
		t.Errorf("empty backend should have nil primary, got %s", m.Primary.Name())
	}
	// 配置有效时按配置选择主后端
	if m := NewManager("brave", "fake-key", ""); m.Primary.Name() != "brave" {
		t.Errorf("brave with key should be brave, got %s", m.Primary.Name())
	}
	if m := NewManager("searxng", "", "http://localhost:8080"); m.Primary.Name() != "searxng" {
		t.Errorf("searxng with url should be searxng, got %s", m.Primary.Name())
	}
	// 备用后端：未选中的已配置接口作为 fallback
	if m := NewManager("searxng", "fake-key", "http://localhost:8080"); m.Fallback == nil || m.Fallback.Name() != "brave" {
		t.Errorf("searxng primary should fallback to brave, got %v", m.Fallback)
	}
}

func TestFormatResults(t *testing.T) {
	results := []Result{
		{Title: "Go 官网", URL: "https://go.dev", Snippet: "下载 Go"},
		{Title: "文档", URL: "https://go.dev/doc"},
	}
	out := FormatResults(results)
	if out == "" {
		t.Fatal("empty output")
	}
}
