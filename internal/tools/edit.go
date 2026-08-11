package tools

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// EditFileTool 在指定文件中查找 old_content 并替换为 new_content（所有匹配处），
// 用于精准的局部修改，比 write_file 整文件覆盖更安全。
type EditFileTool struct{}

func (t *EditFileTool) Name() string { return "edit_file" }
func (t *EditFileTool) Description() string {
	return "在指定文件中查找 old_content 并替换为 new_content（所有匹配处均替换）。用于局部修改文件内容。"
}
func (t *EditFileTool) ArgsSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":        map[string]any{"type": "string", "description": "文件路径"},
			"old_content": map[string]any{"type": "string", "description": "要查找的原内容（必须精确匹配）"},
			"new_content": map[string]any{"type": "string", "description": "替换成的新内容"},
		},
		"required": []string{"path", "old_content"},
	}
}

func (t *EditFileTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	path, _ := args["path"].(string)
	oldContent, _ := args["old_content"].(string)
	newContent, _ := args["new_content"].(string)
	if oldContent == "" {
		return "", fmt.Errorf("old_content 不能为空")
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}

	abs, err := safePath(path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	content := string(data)

	count := strings.Count(content, oldContent)
	if count == 0 {
		return "", fmt.Errorf("在 %s 中未找到要修改的内容", abs)
	}
	updated := strings.ReplaceAll(content, oldContent, newContent)
	if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil {
		return "", err
	}
	TrackModified(abs)
	return fmt.Sprintf("已修改 %s：共替换 %d 处匹配", abs, count), nil
}
