package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go-agent/internal/skill"
)

// ReadSkillTool 返回指定技能的完整指令内容（安装目录 skills/<name>/SKILL.md）。
// 技能不再自动注入系统提示词，Agent 按需调用本工具读取。
type ReadSkillTool struct {
	// Dir 是技能目录；为空时使用 skill.DefaultDir()（exe 所在目录的 skills/）。
	Dir string
}

func (t *ReadSkillTool) Name() string { return "read_skill" }

func (t *ReadSkillTool) Description() string {
	return "返回指定技能的完整指令内容。先用 list_skills 查询技能目录确定技能名，再把技能名作为 skill_name 传入（如 docx、pdf、github、tdd）。"
}

func (t *ReadSkillTool) ArgsSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"skill_name": map[string]any{"type": "string", "description": "技能名，如 docx、pdf、github、tdd"},
		},
		"required": []string{"skill_name"},
	}
}

func (t *ReadSkillTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	name, _ := args["skill_name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("缺少参数 skill_name")
	}
	// 技能名只允许普通字符：禁止路径分隔符与相对路径片段，防目录穿越。
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." || strings.Contains(name, "..") {
		return "", fmt.Errorf("非法技能名 %q", name)
	}

	dir := t.Dir
	if dir == "" {
		dir = skill.DefaultDir()
	}
	path := filepath.Join(dir, name, "SKILL.md")

	// 双保险：解析后路径必须仍位于技能目录内。
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("非法技能名 %q", name)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("技能 %q 不存在或不可读（%v）", name, err)
	}
	return truncate(string(data), maxToolOutput), nil
}
