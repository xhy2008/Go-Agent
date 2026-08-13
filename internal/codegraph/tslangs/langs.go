// Package tslangs 提供基于 tree-sitter 的多语言解析：语言注册表 + 通用 AST 提取器。
// 覆盖原版 codegraph 的主流语言（Go 与其他语言统一走 tree-sitter，类型感知建边在 codegraph 包）。
package tslangs

import (
	"unsafe"

	ts "github.com/tree-sitter/go-tree-sitter"

	csLang "github.com/tree-sitter/tree-sitter-c-sharp/bindings/go"
	cLang "github.com/tree-sitter/tree-sitter-c/bindings/go"
	cppLang "github.com/tree-sitter/tree-sitter-cpp/bindings/go"
	goLang "github.com/tree-sitter/tree-sitter-go/bindings/go"
	javaLang "github.com/tree-sitter/tree-sitter-java/bindings/go"
	jsLang "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	phpLang "github.com/tree-sitter/tree-sitter-php/bindings/go"
	pyLang "github.com/tree-sitter/tree-sitter-python/bindings/go"
	rbLang "github.com/tree-sitter/tree-sitter-ruby/bindings/go"
	rsLang "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	scLang "github.com/tree-sitter/tree-sitter-scala/bindings/go"
	tsLang "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"go-agent/internal/codegraph/tslangs/dart"
	"go-agent/internal/codegraph/tslangs/kotlin"
)

// Lang 描述一种语言的语法绑定（GetLanguage 返回 tree-sitter Language 指针）。
type Lang struct {
	Name string
	Get  func() unsafe.Pointer
}

// registry 全部支持的语言，key 为小写语言名。
var registry = map[string]*Lang{
	"go":         {Name: "go", Get: goLang.Language},
	"typescript": {Name: "typescript", Get: tsLang.LanguageTypescript},
	"tsx":        {Name: "tsx", Get: tsLang.LanguageTSX},
	"javascript": {Name: "javascript", Get: jsLang.Language},
	"python":     {Name: "python", Get: pyLang.Language},
	"java":       {Name: "java", Get: javaLang.Language},
	"c":          {Name: "c", Get: cLang.Language},
	"cpp":        {Name: "cpp", Get: cppLang.Language},
	"csharp":     {Name: "csharp", Get: csLang.Language},
	"php":        {Name: "php", Get: phpLang.LanguagePHP},
	"ruby":       {Name: "ruby", Get: rbLang.Language},
	"rust":       {Name: "rust", Get: rsLang.Language},
	"kotlin":     {Name: "kotlin", Get: kotlin.Language},
	"scala":      {Name: "scala", Get: scLang.Language},
	"dart":       {Name: "dart", Get: dart.Language},
}

// extMap 扩展名（小写，含点）→ 语言名。
var extMap = map[string]string{
	".go": "go",
	".ts": "typescript", ".mts": "typescript", ".cts": "typescript",
	".tsx": "tsx",
	".js":  "javascript", ".mjs": "javascript", ".cjs": "javascript",
	".jsx": "javascript",
	".py":  "python", ".pyw": "python",
	".java": "java",
	".c":    "c", ".h": "c",
	".cpp": "cpp", ".cc": "cpp", ".cxx": "cpp", ".hpp": "cpp", ".hxx": "cpp", ".hh": "cpp",
	".cs":  "csharp",
	".php": "php", ".module": "php", ".inc": "php",
	".rb": "ruby", ".rake": "ruby",
	".rs": "rust",
	".kt": "kotlin", ".kts": "kotlin",
	".scala": "scala", ".sc": "scala",
	".dart": "dart",
}

// ByExt 返回扩展名对应的语言（未知返回 nil）。
func ByExt(ext string) *Lang {
	name, ok := extMap[ext]
	if !ok {
		return nil
	}
	return registry[name]
}

// Language 返回指定语言名的语法（未知返回 nil）。
func Language(name string) *Lang { return registry[name] }

// NewParser 创建绑定该语言的新解析器。
func (l *Lang) NewParser() (*ts.Parser, *ts.Language, error) {
	p := ts.NewParser()
	lang := ts.NewLanguage(l.Get())
	if err := p.SetLanguage(lang); err != nil {
		p.Close()
		return nil, nil, err
	}
	return p, lang, nil
}

// Named 报告该语言是否注册。
func Named(name string) bool { _, ok := registry[name]; return ok }
