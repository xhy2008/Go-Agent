package codegraph

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

func parseFixture(t *testing.T, file string) *FileGraph {
	t.Helper()
	abs := filepath.Join("testdata", "golden", file)
	fg, err := ParseFile(abs, file)
	if err != nil {
		t.Fatalf("ParseFile(%s): %v", file, err)
	}
	return fg
}

func findNode(fg *FileGraph, name string) *RawNode {
	for i := range fg.Nodes {
		if fg.Nodes[i].Name == name {
			return &fg.Nodes[i]
		}
	}
	return nil
}

func refsOf(fg *FileGraph, pred func(RawRef) bool) []RawRef {
	var out []RawRef
	for _, r := range fg.Refs {
		if pred(r) {
			out = append(out, r)
		}
	}
	return out
}

func TestParseFile_Main(t *testing.T) {
	fg := parseFixture(t, "main.go")

	// 符号种类与行号
	expectNode := func(name string, kind Kind, recv string, line int) {
		t.Helper()
		n := findNode(fg, name)
		if n == nil {
			t.Fatalf("节点 %q 未解析到", name)
		}
		if n.Kind != kind {
			t.Errorf("节点 %q: kind=%v, 期望 %v", name, n.Kind, kind)
		}
		if n.Receiver != recv {
			t.Errorf("节点 %q: receiver=%q, 期望 %q", name, n.Receiver, recv)
		}
		if n.Line != line {
			t.Errorf("节点 %q: line=%d, 期望 %d", name, n.Line, line)
		}
	}
	expectNode("Greeter", KindInterface, "", 10)
	expectNode("Hello", KindStruct, "", 16)
	expectNode("NewHello", KindFunc, "", 26)
	expectNode("greeting", KindConst, "", 30)
	expectNode("count", KindVar, "", 32)
	expectNode("main", KindFunc, "", 35)

	// 同名方法 Greet 有两个节点（接口方法 + Hello 方法），Receiver 集合应完整
	var recvs []string
	for i := range fg.Nodes {
		if fg.Nodes[i].Name == "Greet" {
			recvs = append(recvs, fg.Nodes[i].Receiver)
		}
	}
	sort.Strings(recvs)
	if len(recvs) != 2 || recvs[0] != "Greeter" || recvs[1] != "Hello" {
		t.Errorf("Greet 方法 Receiver 集合=%v, 期望 [Greeter Hello]", recvs)
	}

	// 签名摘要（不含接收者与 body）
	if n := findNode(fg, "add"); n != nil {
		t.Errorf("main.go 不应包含 add 节点")
	}

	// 调用引用
	calls := refsOf(fg, func(r RawRef) bool { return r.Kind == EdgeCall })
	hasCall := func(pkg, name string) bool {
		for _, r := range calls {
			if r.Pkg == pkg && r.Name == name {
				return true
			}
		}
		return false
	}
	if !hasCall("", "NewHello") {
		t.Errorf("缺少调用引用 NewHello，实际: %v", calls)
	}
	if !hasCall("", "Greet") {
		t.Errorf("缺少调用引用 Greet（本地方法调用），实际: %v", calls)
	}
	if !hasCall("fmt", "Println") {
		t.Errorf("缺少调用引用 fmt.Println，实际: %v", calls)
	}
	if !hasCall("strings", "ToUpper") {
		t.Errorf("缺少调用引用 strings.ToUpper，实际: %v", calls)
	}

	// 类型/值引用：imports 已记录
	if fg.Imports["fmt"] != "fmt" || fg.Imports["strings"] != "strings" {
		t.Errorf("Imports 记录异常: %v", fg.Imports)
	}
	// 构造函数的返回类型 *Hello → 值引用
	refs := refsOf(fg, func(r RawRef) bool { return r.Kind == EdgeRef && r.Name == "Hello" })
	if len(refs) == 0 {
		t.Errorf("缺少对 Hello 的类型引用")
	}
}

func TestParseFile_Helper(t *testing.T) {
	fg := parseFixture(t, "helper.go")

	// 符号
	for _, want := range []struct {
		name string
		kind Kind
		line int
	}{{"Num", KindType, 5}, {"add", KindFunc, 8}, {"helper", KindFunc, 11}, {"Worker", KindInterface, 16}} {
		n := findNode(fg, want.name)
		if n == nil {
			t.Fatalf("节点 %q 未解析到", want.name)
		}
		if n.Kind != want.kind || n.Line != want.line {
			t.Errorf("节点 %q: kind=%v line=%d, 期望 kind=%v line=%d", want.name, n.Kind, n.Line, want.kind, want.line)
		}
	}

	// 签名摘要：add 的参数与返回值文本
	if n := findNode(fg, "add"); n.Signature != "(a, b int) int" {
		t.Errorf("add 签名=%q, 期望 %q", n.Signature, "(a, b int) int")
	}

	// 调用引用 add（包内函数调用）
	if !hasRef(fg, func(r RawRef) bool { return r.Kind == EdgeCall && r.Name == "add" }) {
		t.Errorf("缺少对 add 的调用引用")
	}
	// 跨包类型引用 fmt 不存在，但 count（同包 var）被引用
	if !hasRef(fg, func(r RawRef) bool { return r.Kind == EdgeRef && r.Name == "count" }) {
		t.Errorf("缺少对 count 的引用")
	}

	// Doc 首行
	if n := findNode(fg, "add"); n.Doc != "add 求和。" {
		t.Errorf("add Doc=%q, 期望 %q", n.Doc, "add 求和。")
	}
}

func hasRef(fg *FileGraph, pred func(RawRef) bool) bool {
	return len(refsOf(fg, pred)) > 0
}

func TestModelDBSaveLoadRoundtrip(t *testing.T) {
	ix := NewIndex()
	ix.Nodes = []Node{
		{ID: 0, File: "main.go", Line: 7, Kind: KindFunc, Name: "add", Signature: "(a, b int) int", Doc: "求和"},
		{ID: 1, File: "main.go", Line: 11, Kind: KindMethod, Name: "Greet", Receiver: "Hello", Embeds: []string{"Base"}},
	}
	ix.Edges = []Edge{{From: 0, To: 1, Kind: EdgeCall, Site: "main.go:8"}}
	ix.ByFile = map[string][]int{"main.go": {0, 1}}
	ix.ByName = map[string][]int{"add": {0}, "Greet": {1}}
	ix.FileFp = map[string]string{"main.go": "fp-1"}
	ix.BuiltAt = time.Now().Round(0)
	ix.VecDim = 2
	ix.Vecs = map[int][]float32{0: {1.5, -2.5}}

	root := t.TempDir()
	db, err := OpenDB(root)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	if err := db.Save(ix); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := db.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	db.Close()

	if !reflect.DeepEqual(ix.Nodes, got.Nodes) {
		t.Errorf("Nodes 往返不一致:\n原: %+v\n得: %+v", ix.Nodes, got.Nodes)
	}
	if !reflect.DeepEqual(ix.Edges, got.Edges) {
		t.Errorf("Edges 往返不一致:\n原: %+v\n得: %+v", ix.Edges, got.Edges)
	}
	if !reflect.DeepEqual(ix.FileFp, got.FileFp) {
		t.Errorf("FileFp 往返不一致: %v vs %v", ix.FileFp, got.FileFp)
	}
	if got.VecDim != 2 || len(got.Vecs) != 1 || got.Vecs[0][0] != 1.5 || got.Vecs[0][1] != -2.5 {
		t.Errorf("Vecs 往返不一致: dim=%d vecs=%v", got.VecDim, got.Vecs)
	}
	if !reflect.DeepEqual(ix.ByName, got.ByName) || !reflect.DeepEqual(ix.ByFile, got.ByFile) {
		t.Errorf("索引映射往返不一致: ByName=%v ByFile=%v", got.ByName, got.ByFile)
	}
}

func TestDBFTS5Search(t *testing.T) {
	ix := NewIndex()
	ix.Nodes = []Node{
		{ID: 0, File: "main.go", Line: 7, Kind: KindFunc, Name: "add", Signature: "(a, b int) int", Doc: "求和"},
		{ID: 1, File: "main.go", Line: 11, Kind: KindMethod, Name: "Greet", Receiver: "Hello", Doc: "打招呼"},
		{ID: 2, File: "util.go", Line: 3, Kind: KindFunc, Name: "helper", Doc: "调用 add"},
	}
	ix.FileFp = map[string]string{"main.go": "fp-1", "util.go": "fp-2"}
	ix.BuiltAt = time.Now().Round(0)

	root := t.TempDir()
	db, err := OpenDB(root)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	if err := db.Save(ix); err != nil {
		t.Fatalf("Save: %v", err)
	}
	ids, err := db.Search("add", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("FTS5 检索 add 无命中")
	}
	// add 与 helper（Doc 含 add）都应命中
	found := map[int]bool{}
	for _, id := range ids {
		found[id] = true
	}
	if !found[0] {
		t.Errorf("FTS5 未命中符号 add（id=0），实际 %v", ids)
	}
}
