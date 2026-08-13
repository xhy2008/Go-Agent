// Package codegraph 提供多语言代码符号索引与图查询能力（tree-sitter 解析 + SQLite 持久化）。
// 数据流：ParseFile（单文件 AST → FileGraph）→ Build（聚合为 Index）→ DB.Save（落盘）→ 查询（FTS5/图遍历）。
package codegraph

import (
	"time"
)

// Kind 表示符号种类。
type Kind int

const (
	KindUnknown   Kind = iota
	KindFunc           // 顶层函数
	KindMethod         // 方法（含接口方法）
	KindType           // 命名类型（非 struct/interface）
	KindInterface      // 接口类型
	KindStruct         // 结构体类型
	KindVar            // 包级变量
	KindConst          // 包级常量
)

func (k Kind) String() string {
	switch k {
	case KindFunc:
		return "func"
	case KindMethod:
		return "method"
	case KindType:
		return "type"
	case KindInterface:
		return "interface"
	case KindStruct:
		return "struct"
	case KindVar:
		return "var"
	case KindConst:
		return "const"
	}
	return "unknown"
}

// EdgeKind 表示两个符号之间的关系种类。
type EdgeKind int

const (
	EdgeUnknown EdgeKind = iota
	EdgeCall             // 函数/方法调用
	EdgeRef              // 名称引用（绑定到定义）
	EdgeImpl             // 接口方法 → 实现方法（动态分派）
)

func (e EdgeKind) String() string {
	switch e {
	case EdgeCall:
		return "call"
	case EdgeRef:
		return "ref"
	case EdgeImpl:
		return "impl"
	}
	return "unknown"
}

// Node 是索引中的一个符号（函数/方法/类型/变量/常量）。
type Node struct {
	ID        int
	File      string // 相对项目根，正斜杠
	Line      int    // 声明行号（1 起）
	Kind      Kind
	Name      string   // 符号名（方法为方法名）
	Receiver  string   // 方法接收者类型名（KindMethod 专用；接口方法为接口名）
	Signature string   // 签名摘要（参数与返回值文本）
	Doc       string   // 注释首行（Doc 检索用）
	Embeds    []string // 结构体嵌入的类型名（pkg 限定），供方法集合并
}

// Edge 是两个符号之间的关系。
type Edge struct {
	From, To int // 节点 ID
	Kind     EdgeKind
	Site     string // 引用处 "file:line"
}

// Index 是构建完成的代码图索引。
type Index struct {
	Nodes   []Node
	Edges   []Edge
	ByFile  map[string][]int  // file → node IDs
	ByName  map[string][]int  // name → node IDs
	FileFp  map[string]string // file → 指纹（增量重建用）
	BuiltAt time.Time
	// Vecs 为可选的全量符号向量（node ID → embedding，由 internal/semantic 填充）。
	// 为空表示未启用语义检索，Explore 保持纯词法行为。
	Vecs   map[int][]float32
	VecDim int
}

// NewIndex 创建空索引。
func NewIndex() *Index {
	return &Index{
		ByFile: make(map[string][]int),
		ByName: make(map[string][]int),
		FileFp: make(map[string]string),
		Vecs:   make(map[int][]float32),
	}
}
