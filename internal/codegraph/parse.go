package codegraph

import "path/filepath"

// RawNode 是单文件解析出的符号（尚未分配全局 ID）。
type RawNode struct {
	Kind      Kind
	Line      int
	Name      string
	Receiver  string
	Signature string
	Doc       string
	Embeds    []string
}

// RawRef 是单文件内的名称引用，待 resolve 阶段绑定到具体 Node。
type RawRef struct {
	Kind  EdgeKind // EdgeCall 或 EdgeRef
	Pkg   string   // 包别名（import 限定），空表示本包/本地
	Name  string   // 标识符名
	Line  int      // 引用处行号（1 起）
	Owner int      // 所属符号在 Nodes 中的下标
}

// FileGraph 是单个源文件的解析产物，供 Build 聚合。
// 全部语言统一由 tree-sitter 通用提取器产出（tslangs 包），此处仅承载传输结构。
type FileGraph struct {
	Path    string // 相对项目根的路径（正斜杠）
	Nodes   []RawNode
	Refs    []RawRef
	Imports map[string]string // 本地别名 → 导入路径（Go 等）
}

func newFileGraph(rel string) *FileGraph {
	return &FileGraph{Path: filepath.ToSlash(rel), Imports: make(map[string]string)}
}
