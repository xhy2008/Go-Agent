package codegraph

import (
	"sort"
	"strings"
	"time"
)

// pkgSyms 是单个包（目录）的符号表。
type pkgSyms struct {
	dir        string
	top        map[string]int   // 包级名称（func/var/const/type）→ 节点 ID
	methods    map[string][]int // 方法名 → 节点 ID 列表
	byRecv     map[string][]int // 接收者类型名 → 方法节点 ID 列表
	typeByName map[string]int   // 类型名 → 类型节点 ID（struct/interface/type）
}

func newPkgSyms(dir string) *pkgSyms {
	return &pkgSyms{
		dir:        dir,
		top:        make(map[string]int),
		methods:    make(map[string][]int),
		byRecv:     make(map[string][]int),
		typeByName: make(map[string]int),
	}
}

// Build 将解析后的文件图聚合为 Index，完成节点 ID 分配、引用解析与接口动态分派。
// root 为项目根（读取源码/对齐绝对路径）；modulePath 为 go.mod 的 module 名。
// 全部语言（含 Go）统一走词法 resolveRef 解析引用；接口实现（impl 边）按方法签名匹配。
func Build(root, modulePath string, fgs []*FileGraph) (*Index, error) {
	sort.Slice(fgs, func(i, j int) bool { return fgs[i].Path < fgs[j].Path })

	ix := NewIndex()
	ix.BuiltAt = time.Now().Round(0)
	fileBase := make(map[string]int, len(fgs))
	pkgs := make(map[string]*pkgSyms)

	// 第一遍：分配节点 ID，建立包符号表
	base := 0
	for _, fg := range fgs {
		dir := dirOf(fg.Path)
		ps := pkgs[dir]
		if ps == nil {
			ps = newPkgSyms(dir)
			pkgs[dir] = ps
		}
		fileBase[fg.Path] = base
		for i := range fg.Nodes {
			rn := &fg.Nodes[i]
			id := base + i
			n := Node{
				ID:        id,
				File:      fg.Path,
				Line:      rn.Line,
				Kind:      rn.Kind,
				Name:      rn.Name,
				Receiver:  rn.Receiver,
				Signature: rn.Signature,
				Doc:       rn.Doc,
				Embeds:    rn.Embeds,
			}
			ix.Nodes = append(ix.Nodes, n)
			ix.ByFile[fg.Path] = append(ix.ByFile[fg.Path], id)
			ix.ByName[n.Name] = append(ix.ByName[n.Name], id)
			switch n.Kind {
			case KindFunc, KindVar, KindConst:
				ps.top[n.Name] = id
			case KindMethod:
				ps.methods[n.Name] = append(ps.methods[n.Name], id)
				ps.byRecv[n.Receiver] = append(ps.byRecv[n.Receiver], id)
			case KindStruct, KindInterface, KindType:
				ps.top[n.Name] = id
				ps.typeByName[n.Name] = id
			}
		}
		base += len(fg.Nodes)
	}

	// 第二遍：解析引用 → 边（全部走词法 resolveRef）。
	var edges []Edge
	for _, fg := range fgs {
		ps := pkgs[dirOf(fg.Path)]
		baseID := fileBase[fg.Path]
		for i := range fg.Refs {
			r := &fg.Refs[i]
			if r.Owner < 0 || r.Owner >= len(fg.Nodes) {
				continue
			}
			from := baseID + r.Owner
			to, ok := resolveRef(fg, r, ps, pkgs, modulePath)
			if !ok || to == from {
				continue
			}
			edges = append(edges, Edge{From: from, To: to, Kind: r.Kind, Site: fg.Path + ":" + itoa(r.Line)})
		}
	}
	ix.Edges = edges

	// 第三遍：接口动态分派（impl 边）
	implEdges(ix, pkgs)

	return ix, nil
}

// resolveRef 将一条引用解析为目标节点 ID。
// v1 词法近似：同名方法存在多个候选时放弃解析（避免错误边），保证精确优先。
func resolveRef(fg *FileGraph, r *RawRef, ps *pkgSyms, pkgs map[string]*pkgSyms, modulePath string) (int, bool) {
	if r.Pkg != "" {
		impPath, ok := fg.Imports[r.Pkg]
		if !ok {
			return 0, false
		}
		dir, ok := importDir(impPath, modulePath)
		if !ok {
			return 0, false // stdlib / 外部依赖：不索引
		}
		target := pkgs[dir]
		if target == nil {
			return 0, false
		}
		if id, ok := target.top[r.Name]; ok {
			return id, true
		}
		if ms := target.methods[r.Name]; len(ms) == 1 {
			return ms[0], true
		}
		return 0, false
	}
	// 本包引用
	if id, ok := ps.top[r.Name]; ok {
		return id, true
	}
	if ms := ps.methods[r.Name]; len(ms) == 1 {
		return ms[0], true
	}
	return 0, false
}

// importDir 将 import 路径映射为项目内相对目录；非本 module 的路径返回 false。
func importDir(importPath, modulePath string) (string, bool) {
	if modulePath == "" {
		return "", false
	}
	if importPath == modulePath {
		return ".", true
	}
	if strings.HasPrefix(importPath, modulePath+"/") {
		return strings.TrimPrefix(importPath, modulePath+"/"), true
	}
	return "", false
}

func dirOf(rel string) string {
	i := strings.LastIndexByte(rel, '/')
	if i < 0 {
		return "."
	}
	return rel[:i]
}

// implEdges 为接口方法建立 → 实现方法 的 EdgeImpl 边。
// 候选类型的方法集 = 直接方法 + 嵌入类型提升的方法（含循环保护）。
func implEdges(ix *Index, pkgs map[string]*pkgSyms) {
	seen := make(map[[3]int]bool)
	add := func(from, to int, kind EdgeKind, site string) {
		key := [3]int{from, to, int(kind)}
		if seen[key] {
			return
		}
		seen[key] = true
		ix.Edges = append(ix.Edges, Edge{From: from, To: to, Kind: kind, Site: site})
	}

	for _, ps := range pkgs {
		for iname, iid := range ps.typeByName {
			if ix.Nodes[iid].Kind != KindInterface {
				continue
			}
			for _, imID := range ps.byRecv[iname] {
				im := ix.Nodes[imID]
				pc, rc := parseSig(im.Signature)
				site := im.File + ":" + itoa(im.Line)
				for tname := range ps.typeByName {
					if tname == iname {
						continue
					}
					for _, mID := range methodSet(ps, ix, tname) {
						m := ix.Nodes[mID]
						if m.Name != im.Name {
							continue
						}
						mpc, mrc := parseSig(m.Signature)
						if mpc == pc && mrc == rc {
							add(imID, mID, EdgeImpl, site)
						}
					}
				}
			}
		}
	}
}

// methodSet 返回类型 tname 的方法集（直接方法 + 嵌入类型提升的方法）。
func methodSet(ps *pkgSyms, ix *Index, tname string) []int {
	var out []int
	seen := make(map[int]bool)
	var visit func(n string, depth int)
	visit = func(n string, depth int) {
		if depth > 8 {
			return // 防循环嵌入
		}
		tid, ok := ps.typeByName[n]
		if !ok {
			return
		}
		for _, mID := range ps.byRecv[n] {
			if !seen[mID] {
				seen[mID] = true
				out = append(out, mID)
			}
		}
		for _, e := range ix.Nodes[tid].Embeds {
			if strings.Contains(e, ".") {
				continue // v1：仅同包嵌入
			}
			visit(e, depth+1)
		}
	}
	visit(tname, 0)
	return out
}

// parseSig 从签名摘要中提取参数个数与返回值个数（忽略参数名，v1 兼容性判定）。
func parseSig(s string) (params, results int) {
	s = strings.TrimSpace(s)
	if s == "" || s[0] != '(' {
		return 0, 0
	}
	pEnd := -1
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				pEnd = i
			}
		}
		if pEnd >= 0 {
			break
		}
	}
	if pEnd < 0 {
		return 0, 0
	}
	params = segCount(s[1:pEnd])
	rest := strings.TrimSpace(s[pEnd+1:])
	switch {
	case rest == "":
		results = 0
	case rest[0] == '(':
		closeIdx := findClose(rest)
		results = segCount(rest[1:closeIdx])
	default:
		results = 1
	}
	return params, results
}

// segCount 统计括号组内逗号分隔的段数（空为 0）。
func segCount(inner string) int {
	inner = strings.TrimSpace(inner)
	if inner == "" {
		return 0
	}
	return countCommas(inner) + 1
}

func countCommas(s string) int {
	depth, n := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ',':
			if depth == 0 {
				n++
			}
		}
	}
	return n
}

func findClose(s string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return len(s) - 1
}

// itoa 最小整数转字符串（避免引入 strconv 依赖）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
