package tslangs

import (
	"fmt"
	"strings"

	ts "github.com/tree-sitter/go-tree-sitter"
)

// Kind 数值与 codegraph 包内 Kind 常量对齐（契约：改动需同步两处）。
const (
	KindUnknown   = 0
	KindFunc      = 1
	KindMethod    = 2
	KindType      = 3
	KindInterface = 4
	KindStruct    = 5
	KindVar       = 6
	KindConst     = 7
)

// EdgeKind 数值与 codegraph 包内 EdgeKind 常量对齐。
const (
	EdgeUnknown = 0
	EdgeCall    = 1
	EdgeRef     = 2
)

// Node 是单文件提取出的符号（平行于 codegraph.RawNode）。
type Node struct {
	Kind      int
	Line      int
	Name      string
	Receiver  string
	Signature string
	Doc       string
	Embeds    []string
}

// Ref 是单文件内的调用/引用（平行于 codegraph.RawRef）。
type Ref struct {
	Kind  int
	Pkg   string
	Name  string
	Line  int
	Owner int
}

// FileGraph 是单个源文件的提取产物。
type FileGraph struct {
	Path    string
	Nodes   []Node
	Refs    []Ref
	Imports map[string]string // 本地别名 → 导入路径（Go 等；跨包引用解析用）
}

// declSpec 描述一种声明节点如何映射为符号。
type declSpec struct {
	kind      int  // 符号 Kind
	container bool // 是容器（类/接口等），其方法接收者取容器名
	skip      bool // 跳过不建符号（import、namespace 等）
	dyn       bool // 需要动态判定 kind（如 Go type_spec → struct/interface/type）
}

// decls 节点类型名 → 声明映射（合并全部注册语言中语义一致的节点类型；
// 同一节点类型名在不同语言中语义冲突时需按语言拆分——当前仅 field_declaration
// 在 Go 中语义不同（struct 字段，不建符号），由 walk 中语言特判处理）。
var decls = map[string]declSpec{
	// TS / JS / TSX / Python / C / C++ / Scala 通用函数声明
	"function_declaration":   {kind: KindFunc},
	"function_definition":    {kind: KindFunc},
	"method_definition":      {kind: KindMethod},
	"class_declaration":      {kind: KindStruct, container: true},
	"class_definition":       {kind: KindStruct, container: true},
	"interface_declaration":  {kind: KindInterface, container: true},
	"type_alias_declaration": {kind: KindType},
	"enum_declaration":       {kind: KindType, container: true},
	"variable_declarator":    {kind: KindVar},
	"lexical_declaration":    {skip: true},
	"import_statement":       {skip: true},
	"import_from_statement":  {skip: true},
	"import_declaration":     {skip: true},
	"package_declaration":    {skip: true},
	"arrow_function":         {skip: true},
	"function_expression":    {skip: true},
	// Python / C / C++ / PHP / Java / C# 等
	"assignment":                {kind: KindVar},
	"method_declaration":        {kind: KindMethod},
	"record_declaration":        {kind: KindStruct, container: true},
	"struct_specifier":          {kind: KindStruct, container: true},
	"class_specifier":           {kind: KindStruct, container: true},
	"union_specifier":           {kind: KindStruct, container: true},
	"enum_specifier":            {kind: KindType, container: true},
	"preproc_include":           {skip: true},
	"namespace_declaration":     {skip: true},
	"using_directive":           {skip: true},
	"property_declaration":      {kind: KindVar},
	"namespace_definition":      {skip: true},
	"namespace_use_declaration": {skip: true},
	"use_declaration":           {skip: true},
	// Ruby
	"method":           {kind: KindMethod},
	"singleton_method": {kind: KindMethod},
	"class":            {kind: KindStruct, container: true},
	"module":           {kind: KindType, container: true},
	// Rust
	"function_item": {kind: KindFunc},
	"struct_item":   {kind: KindStruct, container: true},
	"enum_item":     {kind: KindType, container: true},
	"trait_item":    {kind: KindInterface, container: true},
	"impl_item":     {skip: true, container: true},
	"type_item":     {kind: KindType},
	"const_item":    {kind: KindConst},
	"static_item":   {kind: KindVar},
	"mod_item":      {kind: KindType, container: true},
	"use_item":      {skip: true},
	// Kotlin / Scala / Dart
	"object_declaration": {kind: KindType, container: true},
	"enum_class":         {kind: KindType, container: true},
	"import_header":      {skip: true},
	"trait_definition":   {kind: KindInterface, container: true},
	"object_definition":  {kind: KindType, container: true},
	"enum_definition":    {kind: KindType, container: true},
	"val_definition":     {kind: KindVar},
	"mixin_declaration":  {kind: KindType, container: true},
	"method_signature":   {kind: KindMethod},
	"import_uri":         {skip: true},
	// Go（tree-sitter 无字段名，名字/接收者/签名按节点位置推断）
	"type_spec":              {kind: KindUnknown, dyn: true, container: true},
	"method_elem":            {kind: KindMethod}, // 接口方法
	"const_spec":             {kind: KindConst},
	"var_spec":               {kind: KindVar},
	"type_declaration":       {skip: true},
	"var_declaration":        {skip: true},
	"const_declaration":      {skip: true},
	"package_clause":         {skip: true},
	"field_declaration_list": {skip: true},
	"import_spec":            {skip: true},
}

// calls 语言名 → 调用节点类型名集合。
var calls = map[string][]string{
	"go":         {"call_expression"},
	"typescript": {"call_expression"},
	"tsx":        {"call_expression"},
	"javascript": {"call_expression"},
	"python":     {"call"},
	"java":       {"method_invocation"},
	"c":          {"call_expression"},
	"cpp":        {"call_expression"},
	"csharp":     {"invocation_expression"},
	"php":        {"function_call_expression", "member_call_expression", "scoped_call_expression"},
	"ruby":       {"call"},
	"rust":       {"call_expression"},
	"kotlin":     {"call_expression"},
	"scala":      {"call_expression"},
	"dart":       {"function_expression"},
}

type extractor struct {
	fg         *FileGraph
	src        []byte
	lang       string
	callT      map[string]bool
	imports    map[string]string // 本地别名 → 导入路径（Go 等）
	skipRanges [][2]uint
}

// Extract 解析单个文件并提取符号与调用。
func Extract(path string, l *Lang, src []byte) (*FileGraph, error) {
	p, _, err := l.NewParser()
	if err != nil {
		return nil, err
	}
	defer p.Close()
	tree := p.Parse(src, nil)
	if tree == nil {
		return nil, fmt.Errorf("%s: 解析失败（返回 nil 树）", path)
	}
	defer tree.Close()

	ex := &extractor{
		fg:      &FileGraph{Path: path},
		src:     src,
		lang:    l.Name,
		callT:   make(map[string]bool),
		imports: make(map[string]string),
	}
	for _, k := range calls[l.Name] {
		ex.callT[k] = true
	}
	ex.walk(tree.RootNode(), -1, nil)
	ex.fg.Imports = ex.imports
	return ex.fg, nil
}

func (ex *extractor) text(n *ts.Node) string {
	if n == nil {
		return ""
	}
	b, e := n.ByteRange()
	if b >= e || int(e) > len(ex.src) {
		return ""
	}
	return string(ex.src[b:e])
}

// walk 深度优先遍历 AST。owner 为当前所属符号下标（-1 无）；containers 为容器名栈。
func (ex *extractor) walk(n *ts.Node, owner int, containers []string) {
	if n == nil {
		return
	}
	kind := n.Kind()
	spec, isDecl := decls[kind]
	isCall := ex.callT[kind]

	// Go：结构体字段/接口成员按有无名字区分——具名字段是成员（不建符号），
	// 无名字段/无名字 method_elem 是嵌入类型（记入容器 Embeds）。
	if ex.lang == "go" {
		if kind == "field_declaration" {
			isDecl = false
			if !ex.goHasFieldName(n) {
				ex.goEmbedded(n, owner)
			}
		}
		if kind == "method_elem" && !ex.goHasFieldName(n) {
			isDecl = false // 接口嵌入接口
			ex.goEmbedded(n, owner)
		}
	}

	if isDecl && !spec.skip {
		name := ex.nodeName(n)
		if name != "" && !isAnon(name) {
			// 声明名不产生自引用（Go：名字按子节点位置推断）
			if ex.lang == "go" {
				if nn := ex.nameNode(n); nn != nil {
					ex.skipRanges = append(ex.skipRanges, [2]uint{nn.StartByte(), nn.EndByte()})
				}
			}
			// 动态判定 kind（Go type_spec → struct/interface/type）
			symKind := spec.kind
			if spec.dyn {
				symKind = ex.dynGoKind(n)
			}
			recv := ""
			if symKind == KindMethod {
				recv = ex.containerName(n, containers)
			}
			// 容器（类/接口等）内的函数提升为方法（Python/Ruby/Scala/Kotlin 等
			// 的类成员函数在 tree-sitter 中也是 function 类节点）。
			if symKind == KindFunc && len(containers) > 0 && ex.inContainerBody(n) {
				symKind = KindMethod
				recv = containers[len(containers)-1]
			}
			idx := len(ex.fg.Nodes)
			ex.fg.Nodes = append(ex.fg.Nodes, Node{
				Kind:      symKind,
				Line:      int(n.StartPosition().Row) + 1,
				Name:      name,
				Receiver:  recv,
				Signature: ex.sigOf(n),
				Doc:       ex.docOf(n),
			})
			if spec.container {
				containers = append(containers, name)
			}
			owner = idx
		}
	}

	// Go import 提取：import_spec "path" 或 alias "path"
	if ex.lang == "go" && kind == "import_spec" {
		ex.goImport(n)
	}

	if isCall {
		if ex.lang == "go" {
			// 函数子树（首个命名子节点）不参与引用提取（限定名由 goCallRef 处理）
			if fn := n.NamedChild(0); fn != nil {
				ex.skipRanges = append(ex.skipRanges, [2]uint{fn.StartByte(), fn.EndByte()})
			}
			if t, pkg := ex.goCallRef(n); t != "" {
				ex.fg.Refs = append(ex.fg.Refs, Ref{
					Kind:  EdgeCall,
					Pkg:   pkg,
					Name:  t,
					Line:  int(n.StartPosition().Row) + 1,
					Owner: owner,
				})
			}
		} else {
			if t := ex.callTarget(n); t != "" {
				ex.fg.Refs = append(ex.fg.Refs, Ref{
					Kind:  EdgeCall,
					Name:  t,
					Line:  int(n.StartPosition().Row) + 1,
					Owner: owner,
				})
			}
		}
	}

	// Go 标识符引用（声明名/调用函数子树已在 skipRanges 中排除）
	if ex.lang == "go" && owner >= 0 &&
		(kind == "identifier" || kind == "type_identifier") && !ex.inSkip(n.StartByte()) {
		ex.fg.Refs = append(ex.fg.Refs, Ref{
			Kind:  EdgeRef,
			Name:  strings.TrimSpace(ex.text(n)),
			Line:  int(n.StartPosition().Row) + 1,
			Owner: owner,
		})
	}

	cc := uint(n.NamedChildCount())
	for i := uint(0); i < cc; i++ {
		ex.walk(n.NamedChild(i), owner, containers)
	}
}

// dynGoKind 动态判定 Go type_spec 的符号种类：子节点为 struct_type → Struct，
// interface_type → Interface，其余（别名/泛型约束等）→ Type。
func (ex *extractor) dynGoKind(n *ts.Node) int {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		switch k := n.NamedChild(i).Kind(); k {
		case "struct_type":
			return KindStruct
		case "interface_type":
			return KindInterface
		}
	}
	return KindType
}

// goImport 提取 Go import_spec 的路径与别名（本地别名 → 导入路径）。
// 形式：import "fmt" / import alias "path" / import . "path" / import _ "path"。
func (ex *extractor) goImport(n *ts.Node) {
	cc := uint(n.NamedChildCount())
	var alias, path string
	for i := uint(0); i < cc; i++ {
		c := n.NamedChild(i)
		switch c.Kind() {
		case "package_identifier":
			alias = strings.TrimSpace(ex.text(c))
		case "interpreted_string_literal", "raw_string_literal":
			path = strings.Trim(ex.text(c), `"`)
		}
	}
	if path == "" {
		return
	}
	// 无别名 → 用包路径最后一段
	if alias == "" || alias == "." || alias == "_" {
		if alias == "." || alias == "_" {
			return // 点导入/空白导入不参与限定名解析
		}
		if i := strings.LastIndexByte(path, '/'); i >= 0 {
			alias = path[i+1:]
		} else {
			alias = path
		}
	}
	ex.imports[alias] = path
}

// nameNode 返回声明节点的名称节点（与 nodeName 同序），用于标记跳过自引用。
func (ex *extractor) nameNode(n *ts.Node) *ts.Node {
	if c := n.ChildByFieldName("name"); c != nil && !c.IsMissing() {
		return c
	}
	if c := n.ChildByFieldName("left"); c != nil {
		return c
	}
	if d := n.ChildByFieldName("declarator"); d != nil {
		return d
	}
	cc := uint(n.NamedChildCount())
	for i := uint(0); i < cc; i++ {
		switch c := n.NamedChild(i); c.Kind() {
		case "identifier", "property_identifier", "field_identifier", "name", "simple_identifier":
			return c
		}
	}
	for i := uint(0); i < cc; i++ {
		if n.NamedChild(i).Kind() == "type_identifier" {
			return n.NamedChild(i)
		}
	}
	return nil
}

// goHasFieldName 报告 Go field_declaration/method_elem 是否具名（否则为嵌入类型）。
func (ex *extractor) goHasFieldName(n *ts.Node) bool {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		if n.NamedChild(i).Kind() == "field_identifier" {
			return true
		}
	}
	return false
}

// goEmbedded 将 Go 嵌入类型（struct 嵌入 / interface 嵌入）记入 owner 的 Embeds 并建引用。
func (ex *extractor) goEmbedded(n *ts.Node, owner int) {
	if owner < 0 {
		return
	}
	name := ex.typeIdent(n)
	if name == "" {
		return
	}
	ex.fg.Nodes[owner].Embeds = append(ex.fg.Nodes[owner].Embeds, name)
	ex.fg.Refs = append(ex.fg.Refs, Ref{
		Kind:  EdgeRef,
		Name:  name,
		Line:  int(n.StartPosition().Row) + 1,
		Owner: owner,
	})
}

// goCallRef 提取 Go call_expression 的目标与包限定：
// 函数子树为 selector_expression 且对象是 import 别名时 → (方法名, 别名)；
// 否则 → (标识符, "")。函数子树（首个命名子节点）由调用方标记跳过引用提取。
func (ex *extractor) goCallRef(n *ts.Node) (name, pkg string) {
	fn := n.NamedChild(0)
	if fn == nil {
		return "", ""
	}
	if fn.Kind() == "selector_expression" {
		for i := uint(0); i < fn.NamedChildCount(); i++ {
			c := fn.NamedChild(i)
			switch c.Kind() {
			case "field_identifier":
				name = strings.TrimSpace(ex.text(c))
			case "identifier":
				if _, ok := ex.imports[strings.TrimSpace(ex.text(c))]; ok {
					pkg = strings.TrimSpace(ex.text(c))
				}
			}
		}
		return name, pkg
	}
	return ex.callTarget(n), ""
}

// inSkip 报告字节偏移是否落在跳过区间内（声明名/调用函数子树）。
func (ex *extractor) inSkip(byteOff uint) bool {
	for _, r := range ex.skipRanges {
		if byteOff >= r[0] && byteOff < r[1] {
			return true
		}
	}
	return false
}

func isAnon(name string) bool {
	switch name {
	case "", "anonymous", "Anonymous", "__anon", "lambda":
		return true
	}
	return strings.HasPrefix(name, "(") || strings.HasPrefix(name, "<")
}

// nodeName 提取声明节点的名称（name 字段优先，其次直接子节点标识符，C 系 declarator 兜底）。
func (ex *extractor) nodeName(n *ts.Node) string {
	if c := n.ChildByFieldName("name"); c != nil && !c.IsMissing() {
		if t := strings.TrimSpace(ex.text(c)); t != "" {
			return t
		}
	}
	// assignment / variable_declarator：取 left/name 字段
	if c := n.ChildByFieldName("left"); c != nil {
		if t := ex.deepIdent(c); t != "" {
			return t
		}
	}
	// C 系 function_definition：declarator → identifier
	if d := n.ChildByFieldName("declarator"); d != nil {
		if t := ex.deepIdent(d); t != "" {
			return t
		}
	}
	// 直接命名子节点（identifier 类优先，type_identifier 次之），不穿透 body 容器
	if t := ex.firstChildIdent(n); t != "" {
		return t
	}
	// 兜底：递归穿透
	return ex.deepIdent(n)
}

// firstChildIdent 只扫描节点的直接命名子节点（identifier 类优先，type_identifier 次之）。
func (ex *extractor) firstChildIdent(n *ts.Node) string {
	cc := uint(n.NamedChildCount())
	// 第一遍：identifier 类
	for i := uint(0); i < cc; i++ {
		if t := ex.textIfKind(n.NamedChild(i), true); t != "" {
			return t
		}
	}
	// 第二遍：type_identifier
	for i := uint(0); i < cc; i++ {
		if t := ex.textIfKind(n.NamedChild(i), false); t != "" {
			return t
		}
	}
	return ""
}

func (ex *extractor) textIfKind(n *ts.Node, preferIdent bool) string {
	if n == nil {
		return ""
	}
	k := n.Kind()
	if preferIdent {
		switch k {
		case "identifier", "property_identifier", "field_identifier", "name", "simple_identifier":
			return strings.TrimSpace(ex.text(n))
		}
		return ""
	}
	if k == "type_identifier" {
		return strings.TrimSpace(ex.text(n))
	}
	return ""
}

// deepIdent 深度优先查找标识符节点文本。
// 两遍：先找 identifier 类（函数/方法名，含 simple_identifier），
// 再找 type_identifier（类型声明名），避免把返回类型当函数名。
func (ex *extractor) deepIdent(n *ts.Node) string {
	if n == nil {
		return ""
	}
	if t := ex.findKind(n, true); t != "" {
		return t
	}
	return ex.findKind(n, false)
}

func (ex *extractor) findKind(n *ts.Node, preferIdent bool) string {
	if n == nil {
		return ""
	}
	k := n.Kind()
	switch k {
	case "identifier", "property_identifier", "field_identifier", "name", "simple_identifier":
		if preferIdent {
			return strings.TrimSpace(ex.text(n))
		}
	case "type_identifier":
		if !preferIdent {
			return strings.TrimSpace(ex.text(n))
		}
	}
	cc := uint(n.NamedChildCount())
	for i := uint(0); i < cc; i++ {
		if t := ex.findKind(n.NamedChild(i), preferIdent); t != "" {
			return t
		}
	}
	return ""
}

// inContainerBody 检查函数是否直接位于容器体内：从父节点向上，
// 遇到容器（class/interface 等）之前不经过任何函数节点（避免嵌套函数误判为方法）。
func (ex *extractor) inContainerBody(n *ts.Node) bool {
	for p := n.Parent(); p != nil; p = p.Parent() {
		k := p.Kind()
		if spec, ok := decls[k]; ok {
			if spec.container {
				return true
			}
			if spec.kind == KindFunc || spec.kind == KindMethod {
				return false // 嵌套在函数内：非方法
			}
			continue
		}
		// 非声明节点（block/class_body/statements 等）：继续向上
		switch k {
		case "function_definition", "function_declaration", "function_item",
			"method_definition", "arrow_function", "function_expression":
			return false
		}
	}
	return false
}

// containerName 返回方法所属容器的名称（最近容器栈顶；rust impl 取 type 字段；
// Go method_declaration 从 receiver parameter_list 提取）。
func (ex *extractor) containerName(n *ts.Node, containers []string) string {
	if ex.lang == "go" && n.Kind() == "method_declaration" {
		if r := ex.goReceiver(n); r != "" {
			return r
		}
	}
	if len(containers) > 0 {
		// rust impl_item 场景：容器名为 impl 的 type 字段
		if p := n.Parent(); p != nil && p.Kind() == "impl_item" {
			if t := p.ChildByFieldName("type"); t != nil {
				if s := strings.TrimSpace(ex.text(t)); s != "" {
					return s
				}
			}
		}
		return containers[len(containers)-1]
	}
	// 兜底：直接父容器
	if p := n.Parent(); p != nil {
		spec, ok := decls[p.Kind()]
		if ok && spec.container {
			return ex.nodeName(p)
		}
	}
	return ""
}

// goReceiver 提取 Go method_declaration 的接收者类型：
// 首个 parameter_list > parameter_declaration > (pointer_type|type_identifier)。
func (ex *extractor) goReceiver(n *ts.Node) string {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c.Kind() != "parameter_list" {
			continue
		}
		// 该 parameter_list 是 receiver（方法第一个参数列表）
		for j := uint(0); j < c.NamedChildCount(); j++ {
			pd := c.NamedChild(j)
			if pd.Kind() != "parameter_declaration" {
				continue
			}
			for k := uint(0); k < pd.NamedChildCount(); k++ {
				t := pd.NamedChild(k)
				switch t.Kind() {
				case "type_identifier":
					if s := strings.TrimSpace(ex.text(t)); s != "" {
						return s
					}
				case "pointer_type", "qualified_type", "generic_type":
					if s := ex.typeIdent(t); s != "" {
						return s
					}
				}
			}
		}
		return ""
	}
	return ""
}

// typeIdent 在类型表达式子树中找最内层的 type_identifier。
func (ex *extractor) typeIdent(n *ts.Node) string {
	if n == nil {
		return ""
	}
	if n.Kind() == "type_identifier" {
		return strings.TrimSpace(ex.text(n))
	}
	for i := uint(0); i < n.NamedChildCount(); i++ {
		if s := ex.typeIdent(n.NamedChild(i)); s != "" {
			return s
		}
	}
	return ""
}

// callTarget 从调用节点提取目标函数名。
// member 调用 obj.method()：取 property（最右）；普通调用 f()：取标识符本身。
func (ex *extractor) callTarget(n *ts.Node) string {
	fn := n.ChildByFieldName("function")
	if fn == nil {
		fn = n.ChildByFieldName("name") // java method_invocation / ruby
	}
	if fn == nil {
		fn = n.ChildByFieldName("method") // ruby obj.method
	}
	if fn == nil {
		fn = n.Child(0)
	}
	if fn == nil {
		return ""
	}
	// 成员访问链：取最右侧的标识符（obj.a.b() → b）
	var last string
	var find func(x *ts.Node)
	find = func(x *ts.Node) {
		if x == nil {
			return
		}
		k := x.Kind()
		if k == "identifier" || k == "property_identifier" || k == "field_identifier" {
			last = strings.TrimSpace(ex.text(x))
			return // 该子树内最右即本节点
		}
		cc := uint(x.NamedChildCount())
		for i := uint(0); i < cc; i++ {
			find(x.NamedChild(i))
		}
	}
	// 对 member_expression 等容器：遍历所有命名子节点取最右标识符；
	// 对纯 identifier：直接取文本。
	find(fn)
	if last == "" {
		last = strings.TrimSpace(ex.text(fn))
	}
	return last
}

// sigOf 提取函数签名摘要：name 之后到 body 之前的文本（参数/返回类型）。
// 仅对函数/方法类节点（有 parameters 字段）提取；变量声明返回空。
func (ex *extractor) sigOf(n *ts.Node) string {
	if ex.lang == "go" {
		return ex.goSigOf(n)
	}
	if n.ChildByFieldName("parameters") == nil {
		return ""
	}
	name := n.ChildByFieldName("name")
	body := n.ChildByFieldName("body")
	if body == nil {
		// 无 body（如接口方法/声明）：取 name 之后整行
		if name != nil {
			return ex.lineAfter(name.EndByte())
		}
		return ""
	}
	if name == nil {
		// C 系：declarator 结束到 body 开始
		d := n.ChildByFieldName("declarator")
		if d == nil {
			return ""
		}
		return ex.cleanBetween(d.EndByte(), body.StartByte())
	}
	return ex.cleanBetween(name.EndByte(), body.StartByte())
}

// goSigOf 提取 Go 函数/方法签名（tree-sitter-go 无字段名，按子节点位置推断）：
// 名字（identifier/field_identifier 直接子节点）之后到 body（block）之前的文本；
// 无 body（接口方法 method_elem）取到节点结束；非函数类节点返回空。
func (ex *extractor) goSigOf(n *ts.Node) string {
	hasParams := false
	var nameEnd uint
	var block *ts.Node
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		switch c.Kind() {
		case "parameter_list":
			hasParams = true
		case "block":
			block = c
		case "identifier", "field_identifier":
			if nameEnd == 0 {
				nameEnd = c.EndByte()
			}
		}
	}
	if !hasParams || nameEnd == 0 {
		return ""
	}
	if block != nil {
		return ex.cleanBetween(nameEnd, block.StartByte())
	}
	return ex.cleanBetween(nameEnd, n.EndByte())
}

func (ex *extractor) cleanBetween(start, end uint) string {
	if start >= end || int(end) > len(ex.src) {
		return ""
	}
	s := strings.TrimSpace(string(ex.src[start:end]))
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

func (ex *extractor) lineAfter(byteOff uint) string {
	// 从 byteOff 到行尾
	i := int(byteOff)
	for i < len(ex.src) && ex.src[i] != '\n' {
		i++
	}
	s := strings.TrimSpace(string(ex.src[byteOff:i]))
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// docOf 提取声明前一相邻 comment 节点的首行。
func (ex *extractor) docOf(n *ts.Node) string {
	for s := n.PrevNamedSibling(); s != nil; s = s.PrevNamedSibling() {
		if s.Kind() != "comment" {
			return ""
		}
		t := strings.TrimSpace(ex.text(s))
		t = strings.TrimLeft(t, "/#* \t")
		t = strings.TrimSpace(t)
		if i := strings.IndexByte(t, '\n'); i >= 0 {
			t = strings.TrimSpace(t[:i])
		}
		if len(t) > 160 {
			t = t[:160] + "…"
		}
		return t
	}
	return ""
}
