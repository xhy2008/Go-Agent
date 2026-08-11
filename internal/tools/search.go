package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SearchFileNamesTool 在目录树中按正则表达式匹配文件名（不含路径），
// 适合在大量文件中定位目标文件。
type SearchFileNamesTool struct{}

func (t *SearchFileNamesTool) Name() string { return "search_file_names" }
func (t *SearchFileNamesTool) Description() string {
	return "在目录树中按正则表达式匹配文件名（不含路径），返回匹配的文件列表。适合在大量文件中检索目标文件。"
}
func (t *SearchFileNamesTool) ArgsSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":  map[string]any{"type": "string", "description": "搜索的根目录，默认当前目录"},
			"regex": map[string]any{"type": "string", "description": "匹配文件名的正则表达式，如 ^main\\.go$ 或 _test"},
		},
		"required": []string{"regex"},
	}
}

func (t *SearchFileNamesTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	root, _ := args["path"].(string)
	if root == "" {
		root = "."
	}
	pattern, _ := args["regex"].(string)

	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("无效的正则表达式: %v", err)
	}

	abs, err := safePath(root)
	if err != nil {
		return "", err
	}

	var matches []string
	err = filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", ".idea", ".vscode", "dist", "build", "bin", "obj":
				return filepath.SkipDir
			}
			return nil
		}
		if re.MatchString(d.Name()) {
			rel, err := filepath.Rel(abs, path)
			if err != nil {
				rel = path
			}
			matches = append(matches, rel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	if len(matches) == 0 {
		return "没有找到文件名匹配 " + pattern + " 的文件", nil
	}
	var out strings.Builder
	fmt.Fprintf(&out, "找到 %d 个文件:\n", len(matches))
	for _, m := range matches {
		out.WriteString(m + "\n")
	}
	return truncate(out.String(), maxToolOutput), nil
}

// SearchFileContentTool 在指定的单个文件中按正则表达式搜索内容，
// 返回 行号:内容 列表。
type SearchFileContentTool struct{}

func (t *SearchFileContentTool) Name() string { return "search_file_content" }
func (t *SearchFileContentTool) Description() string {
	return "在指定的单个文件中按正则表达式搜索内容，返回 行号:内容。若需在整个目录中定位，先用 search_file_names 找到文件。"
}
func (t *SearchFileContentTool) ArgsSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":  map[string]any{"type": "string", "description": "要检索的文件路径"},
			"regex": map[string]any{"type": "string", "description": "要匹配的正则表达式"},
		},
		"required": []string{"path", "regex"},
	}
}

func (t *SearchFileContentTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	path, _ := args["path"].(string)
	pattern, _ := args["regex"].(string)

	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("无效的正则表达式: %v", err)
	}

	abs, err := safePath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s 是目录，请指定单个文件", abs)
	}
	if info.Size() > 1<<20 {
		return "", fmt.Errorf("文件 %s 超过 1MB，请使用 read_file_range 分段查看", abs)
	}

	f, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var out strings.Builder
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 0
	count := 0
	for scanner.Scan() {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		lineNo++
		line := scanner.Text()
		if re.MatchString(line) {
			out.WriteString(fmt.Sprintf("%d\t%s\n", lineNo, strings.TrimSpace(line)))
			count++
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if count == 0 {
		return "在 " + abs + " 中没有找到匹配的内容", nil
	}
	fmt.Fprintf(&out, "\n共 %d 处匹配", count)
	return truncate(out.String(), maxToolOutput), nil
}
