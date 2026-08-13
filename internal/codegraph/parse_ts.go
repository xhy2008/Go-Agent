package codegraph

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go-agent/internal/codegraph/tslangs"
)

// sourceExts 支持索引的源文件扩展名（全部语言统一走 tree-sitter 通用提取器）。
var sourceExts = map[string]bool{
	".go": true,
	// TS / JS
	".ts": true, ".mts": true, ".cts": true, ".tsx": true,
	".js": true, ".mjs": true, ".cjs": true, ".jsx": true,
	// Python / Java / C / C++ / C# / PHP / Ruby / Rust / Kotlin / Scala / Dart
	".py": true, ".pyw": true, ".java": true,
	".c": true, ".h": true, ".cpp": true, ".cc": true, ".cxx": true,
	".hpp": true, ".hxx": true, ".hh": true, ".cs": true,
	".php": true, ".module": true, ".inc": true,
	".rb": true, ".rake": true, ".rs": true,
	".kt": true, ".kts": true, ".scala": true, ".sc": true,
	".dart": true,
}

// ParseFile 解析单个源文件为 FileGraph：全部语言统一走 tree-sitter 通用提取器。
// abs 为绝对路径，rel 为相对项目根的路径。
func ParseFile(abs, rel string) (*FileGraph, error) {
	if !sourceExts[strings.ToLower(filepath.Ext(rel))] {
		return nil, fmt.Errorf("不支持的源文件类型: %s", rel)
	}
	return parseTSFile(abs, rel)
}

// parseTSFile 用 tree-sitter 通用提取器解析源文件，并转换为 FileGraph。
func parseTSFile(abs, rel string) (*FileGraph, error) {
	lang := tslangs.ByExt(strings.ToLower(filepath.Ext(rel)))
	if lang == nil {
		return nil, fmt.Errorf("不支持的语言: %s", rel)
	}
	src, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	g, err := tslangs.Extract(rel, lang, src)
	if err != nil {
		return nil, err
	}
	fg := newFileGraph(rel)
	fg.Imports = g.Imports
	for i := range g.Nodes {
		n := &g.Nodes[i]
		fg.Nodes = append(fg.Nodes, RawNode{
			Kind:      Kind(n.Kind),
			Line:      n.Line,
			Name:      n.Name,
			Receiver:  n.Receiver,
			Signature: n.Signature,
			Doc:       n.Doc,
			Embeds:    n.Embeds,
		})
	}
	for i := range g.Refs {
		r := &g.Refs[i]
		fg.Refs = append(fg.Refs, RawRef{
			Kind:  EdgeKind(r.Kind),
			Pkg:   r.Pkg,
			Name:  r.Name,
			Line:  r.Line,
			Owner: r.Owner,
		})
	}
	return fg, nil
}
