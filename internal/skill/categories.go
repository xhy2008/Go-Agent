package skill

// defaultCategories 内置技能分类表。分类说明：
//   - code      常驻：注入系统提示词，模型随时可用（调试、测试、代码审查等）
//   - workflow  HOTL 工作流类：按需查询（计划、执行、审查流程等）
//   - github    GitHub 相关：按需查询（PR、CI、评审意见等）
//   - document  文档处理（docx/pdf/pptx/xlsx/html 等）：按需查询
//   - research  研究类：按需查询
//   - other     未收录技能：按需查询
//
// 单个技能可在 SKILL.md frontmatter 中用 category 字段覆盖（优先于此表）。
var defaultCategories = map[string]string{
	"brainstorming":                   "workflow",
	"code-review":                     "code",
	"doc-writing-guide":               "document",
	"document-review":                 "workflow",
	"docx":                            "document",
	"executing-plans":                 "workflow",
	"finishing-a-development-branch":  "workflow",
	"gh-address-comments":             "github",
	"gh-fix-ci":                       "github",
	"github":                          "github",
	"html-deck":                       "document",
	"html-report":                     "document",
	"loop-execution":                  "workflow",
	"pdf":                             "document",
	"pptx":                            "document",
	"pr-reviewing":                    "github",
	"receiving-code-review":           "code",
	"requesting-code-review":          "code",
	"research-guide":                  "research",
	"resuming":                        "workflow",
	"setup-project":                   "workflow",
	"skill-authoring":                 "workflow",
	"skill-creator":                   "workflow",
	"systematic-debugging":            "code",
	"tdd":                             "code",
	"using-hotl":                      "workflow",
	"verification-before-completion":  "code",
	"writing-plans":                   "workflow",
	"xlsx":                            "document",
	"yeet":                            "github",
}

// defaultCategory 返回技能名称的默认分类；未收录时归为 other（按需查询）。
func defaultCategory(name string) string {
	if c, ok := defaultCategories[name]; ok {
		return c
	}
	return "other"
}
