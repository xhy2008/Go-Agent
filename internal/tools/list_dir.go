package tools

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
)

// ListDirTool 列出目录内容（名称、类型、大小），可按名称模糊过滤。
type ListDirTool struct{}

func (t *ListDirTool) Name() string { return "list_dir" }
func (t *ListDirTool) Description() string {
	return "列出指定目录下的条目（子目录以 / 结尾，文件附带大小）。pattern 可选，按名称模糊过滤（大小写不敏感）。用于了解项目目录结构。"
}
func (t *ListDirTool) ArgsSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string", "description": "目录路径"},
			"pattern": map[string]any{"type": "string", "description": "可选：按名称模糊过滤"},
		},
		"required": []string{"path"},
	}
}

func (t *ListDirTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	path, _ := args["path"].(string)
	abs, err := safePath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s 不是目录", abs)
	}
	pattern, _ := args["pattern"].(string)
	pattern = strings.ToLower(strings.TrimSpace(pattern))

	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	type item struct {
		name string
		dir  bool
		size int64
	}
	var items []item
	for _, e := range entries {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if pattern != "" && !strings.Contains(strings.ToLower(e.Name()), pattern) {
			continue
		}
		it := item{name: e.Name(), dir: e.IsDir()}
		if !e.IsDir() {
			if fi, err := e.Info(); err == nil {
				it.size = fi.Size()
			}
		}
		items = append(items, it)
	}
	// 目录在前，文件在后（目录内部仍按名称有序）。
	sort.SliceStable(items, func(i, j int) bool { return items[i].dir && !items[j].dir })

	var b strings.Builder
	fmt.Fprintf(&b, "%s/（%d 项）\n", abs, len(items))
	for _, it := range items {
		if it.dir {
			b.WriteString("  " + it.name + "/\n")
		} else {
			fmt.Fprintf(&b, "  %s  (%d B)\n", it.name, it.size)
		}
	}
	return strings.TrimSpace(b.String()), nil
}
