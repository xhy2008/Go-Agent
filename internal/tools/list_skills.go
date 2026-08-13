package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"go-agent/internal/skill"
)

// ListSkillsTool 列出/搜索可用技能目录（名称、分类、一句话描述）。
// 只有 code 分类的技能常驻注入系统提示词；其余分类由模型先用本工具发现，
// 再用 read_skill 读取所选技能的完整指令。
type ListSkillsTool struct {
	// Skills 返回当前加载的技能列表（随 /skills reload 热更新自动反映最新状态）。
	Skills func() []*skill.Skill
}

// NewListSkillsTool 创建一个技能目录查询工具。
func NewListSkillsTool(skills func() []*skill.Skill) *ListSkillsTool {
	return &ListSkillsTool{Skills: skills}
}

func (t *ListSkillsTool) Name() string { return "list_skills" }

func (t *ListSkillsTool) Description() string {
	return "列出或搜索可用技能目录，返回每个技能的名称、分类与一句话描述。query 为空返回全部；传入关键词按名称/分类/描述过滤。确定需要的技能后，再用 read_skill 获取其完整指令。"
}

func (t *ListSkillsTool) ArgsSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "搜索关键词（可为空）：按技能名称、分类（code/workflow/github/document/research/other）或描述过滤",
			},
		},
	}
}

func (t *ListSkillsTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	q := ""
	if v, ok := args["query"].(string); ok {
		q = strings.ToLower(strings.TrimSpace(v))
	}

	skills := t.Skills()
	matches := make([]*skill.Skill, 0, len(skills))
	for _, s := range skills {
		if q == "" ||
			strings.Contains(strings.ToLower(s.Name), q) ||
			strings.Contains(strings.ToLower(s.Category), q) ||
			strings.Contains(strings.ToLower(s.Description), q) {
			matches = append(matches, s)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Name < matches[j].Name })

	if len(matches) == 0 {
		return "没有匹配的技能。", nil
	}
	var b strings.Builder
	b.WriteString("技能目录（category=code 为常驻技能；其余先用 read_skill 读取完整指令后再使用）：\n")
	for _, s := range matches {
		b.WriteString(fmt.Sprintf("- %s [%s]: %s\n", s.Name, s.Category, s.Description))
	}
	return strings.TrimSpace(b.String()), nil
}
