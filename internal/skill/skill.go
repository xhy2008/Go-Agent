// Package skill 实现 Skill 加载机制：从 <项目根>/.agent/skills/ 目录
// 扫描 Markdown 文件，解析 YAML Frontmatter，按触发词动态注入上下文。
package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Skill 是一个技能定义。
type Skill struct {
	Name        string   // 技能名
	Description string   // 描述（注入系统消息）
	Triggers    []string // 触发词
	Version     string   // 版本
	Body        string   // 正文（角色指令，触发时注入）
	Path        string   // 来源文件
}

// DefaultDir 返回默认技能目录：<工作目录>/.agent/skills。
func DefaultDir() string {
	wd, _ := os.Getwd()
	return filepath.Join(wd, ".agent", "skills")
}

// Load 扫描目录下的全部 .md 技能文件。
// root 为空时使用 DefaultDir()；目录不存在时返回空列表（不报错）。
func Load(root string) ([]*Skill, error) {
	if root == "" {
		root = DefaultDir()
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, nil // 技能目录不存在时静默跳过
	}

	var skills []*Skill
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		s, err := ParseFile(path)
		if err != nil {
			return fmt.Errorf("解析技能 %s 失败: %w", path, err)
		}
		if s.Name != "" {
			skills = append(skills, s)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return skills, nil
}

// ParseFile 解析单个技能文件。
func ParseFile(path string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(string(data), path), nil
}

// Parse 解析技能内容（YAML Frontmatter + Markdown 正文）。
// frontmatter 格式：
//
//	---
//	name: "python-expert"
//	description: "..."
//	triggers: ["python", "pip", "venv"]
//	version: "1.0"
//	---
//	# 角色指令
//	...
func Parse(content, path string) *Skill {
	s := &Skill{Path: path}
	body := strings.TrimSpace(content)

	// 提取 frontmatter
	if strings.HasPrefix(body, "---") {
		if idx := strings.Index(body[3:], "---"); idx >= 0 {
			fm := body[3 : 3+idx]
			s.Body = strings.TrimSpace(body[3+idx+3:])
			s.parseFrontmatter(fm)
		}
	}
	if s.Body == "" {
		s.Body = body // 无 frontmatter 时整篇作为正文
	}
	return s
}

// parseFrontmatter 逐行解析 frontmatter 中的 key: value。
func (s *Skill) parseFrontmatter(fm string) {
	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`) // 去掉引号

		switch key {
		case "name":
			s.Name = val
		case "description":
			s.Description = val
		case "version":
			s.Version = val
		case "triggers":
			s.Triggers = parseList(val)
		}
	}
}

// parseList 解析 ["a", "b"] 或 a, b 形式的列表。
func parseList(v string) []string {
	v = strings.Trim(v, "[]")
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.Trim(strings.TrimSpace(p), `"'`)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Match 判断用户输入是否命中该技能的任何触发词。
func (s *Skill) Match(text string) bool {
	lower := strings.ToLower(text)
	for _, t := range s.Triggers {
		if strings.Contains(lower, strings.ToLower(t)) {
			return true
		}
	}
	return false
}

// Describe 生成用于系统消息的技能摘要。
func (s *Skill) Describe() string {
	var b strings.Builder
	b.WriteString("- " + s.Name)
	if s.Description != "" {
		b.WriteString(": " + s.Description)
	}
	if len(s.Triggers) > 0 {
		b.WriteString("（触发词: " + strings.Join(s.Triggers, ", ") + "）")
	}
	return b.String()
}
