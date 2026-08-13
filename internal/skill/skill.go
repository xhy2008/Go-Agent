// Package skill 实现 Skill 加载机制：从 <项目根>/.agent/skills/ 目录
// 扫描 Markdown 文件，解析 YAML Frontmatter，按触发词动态注入上下文。
package skill

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go-agent/internal/logx"
)

// ResidentCategory 常驻分类：该分类的技能默认注入系统提示词，其余分类由模型按需查询。
const ResidentCategory = "code"

// Skill 是一个技能定义。
type Skill struct {
	Name        string // 技能名
	Description string // 描述（注入系统消息/技能目录，取首句精简）
	Version     string // 版本
	Category    string // 分类：code=常驻，其余（workflow/github/document/research/other）按需查询
	Body        string // 正文（角色指令，由 Agent 按需读取文件获取）
	Path        string // 来源文件
}

// IsResident 报告技能是否属于常驻分类（默认注入系统提示词）。
func (s *Skill) IsResident() bool { return s.Category == ResidentCategory }

// DefaultDir 返回技能目录：<exe 所在目录>/skills。
// 技能随程序安装目录部署，不依赖进程工作目录（工作目录随启动位置变化）。
func DefaultDir() string {
	exe, err := os.Executable()
	if err != nil {
		return filepath.Join(".", "skills") // 兜底：当前目录
	}
	return filepath.Join(filepath.Dir(exe), "skills")
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
			// 单个文件损坏（读取失败）不拖垮整个技能库：跳过并告警。
			logx.Warn("跳过无法读取的技能文件 %s: %v", path, err)
			return nil
		}
		if s.Name != "" {
			skills = append(skills, s)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// 按技能名排序，保证系统消息中的技能摘要与注入顺序确定性；同名技能去重（保留首个）。
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return dedupeByName(skills), nil
}

// dedupeByName 按技能名去重，保留同名首个技能（避免重复定义导致指令冲突）。
func dedupeByName(skills []*Skill) []*Skill {
	seen := make(map[string]string, len(skills)) // name -> 首次出现文件路径
	out := make([]*Skill, 0, len(skills))
	for _, s := range skills {
		if first, ok := seen[s.Name]; ok {
			logx.Warn("技能 %q 重复定义，忽略 %s（保留 %s）", s.Name, s.Path, first)
			continue
		}
		seen[s.Name] = s.Path
		out = append(out, s)
	}
	return out
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
	// 未显式声明分类时，套用内置默认分类表（未收录的技能归为 other 按需查询）。
	if s.Category == "" {
		s.Category = defaultCategory(s.Name)
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
		case "category":
			s.Category = val
		}
	}
}

// Describe 生成用于系统消息的技能摘要：仅技能名。
// 完整指令由 Agent 按需调用 read_skill 工具读取。
func (s *Skill) Describe() string {
	return "- " + s.Name
}
