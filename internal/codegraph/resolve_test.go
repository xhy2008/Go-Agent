package codegraph

import (
	"path/filepath"
	"testing"
)

func buildGolden(t *testing.T) *Index {
	t.Helper()
	var fgs []*FileGraph
	for _, f := range []string{"helper.go", "main.go"} {
		abs := filepath.Join("testdata", "golden", f)
		fg, err := ParseFile(abs, f)
		if err != nil {
			t.Fatalf("ParseFile(%s): %v", f, err)
		}
		fgs = append(fgs, fg)
	}
	ix, err := Build("", "", fgs)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return ix
}

func nodeID(t *testing.T, ix *Index, name string) int {
	t.Helper()
	ids := ix.ByName[name]
	if len(ids) != 1 {
		t.Fatalf("符号 %q 节点数=%d, 期望 1（IDs=%v）", name, len(ids), ids)
	}
	return ids[0]
}

func edgeExists(ix *Index, from, to int, k EdgeKind) bool {
	for _, e := range ix.Edges {
		if e.From == from && e.To == to && e.Kind == k {
			return true
		}
	}
	return false
}

func TestBuild_Nodes(t *testing.T) {
	ix := buildGolden(t)

	// helper.go(8) + main.go(8) = 16 节点
	if len(ix.Nodes) != 16 {
		t.Fatalf("节点总数=%d, 期望 16", len(ix.Nodes))
	}

	// ByName 索引：Greet 同名两个方法
	if ids := ix.ByName["Greet"]; len(ids) != 2 {
		t.Errorf("ByName[Greet]=%v, 期望 2 个", ids)
	}
	// ByFile 索引
	if ids := ix.ByFile["main.go"]; len(ids) != 8 {
		t.Errorf("ByFile[main.go]=%v, 期望 8 个", ids)
	}
}

func TestBuild_Edges(t *testing.T) {
	ix := buildGolden(t)

	mainID := nodeID(t, ix, "main")
	addID := nodeID(t, ix, "add")
	newHelloID := nodeID(t, ix, "NewHello")
	greetMeID := nodeID(t, ix, "GreetMe")
	greetingID := nodeID(t, ix, "greeting")
	countID := nodeID(t, ix, "count")
	addNumID := nodeID(t, ix, "addNum")

	// 包内函数调用：main → NewHello
	if !edgeExists(ix, mainID, newHelloID, EdgeCall) {
		t.Error("缺少 main → NewHello 调用边")
	}
	// helper() → add()
	helperID := nodeID(t, ix, "helper")
	if !edgeExists(ix, helperID, addID, EdgeCall) {
		t.Error("缺少 helper → add 调用边")
	}
	// 跨文件方法调用：main → GreetMe（唯一方法名，跨文件解析）
	if !edgeExists(ix, mainID, greetMeID, EdgeCall) {
		t.Error("缺少 main → GreetMe 跨文件方法调用边")
	}

	// 值引用：main → greeting / count；addNum → count
	if !edgeExists(ix, mainID, greetingID, EdgeRef) {
		t.Error("缺少 main → greeting 引用边")
	}
	if !edgeExists(ix, mainID, countID, EdgeRef) {
		t.Error("缺少 main → count 引用边")
	}
	if !edgeExists(ix, addNumID, countID, EdgeRef) {
		t.Error("缺少 addNum → count 引用边")
	}

	// 歧义方法调用 g.Greet()（Greeter.Greet 与 Hello.Greet 同名）应跳过
	greeterGreetID := -1
	helloGreetID := -1
	for _, id := range ix.ByName["Greet"] {
		if ix.Nodes[id].Receiver == "Greeter" {
			greeterGreetID = id
		} else {
			helloGreetID = id
		}
	}
	if edgeExists(ix, mainID, greeterGreetID, EdgeCall) || edgeExists(ix, mainID, helloGreetID, EdgeCall) {
		t.Error("歧义方法调用 g.Greet() 不应产生调用边（v1 词法近似：多候选放弃解析）")
	}
}

func TestBuild_ImplEdges(t *testing.T) {
	ix := buildGolden(t)

	// Greeter.Greet（接口方法）→ Hello.Greet（实现方法）
	var imID, implID int
	for _, id := range ix.ByName["Greet"] {
		if ix.Nodes[id].Receiver == "Greeter" {
			imID = id
		} else {
			implID = id
		}
	}
	if !edgeExists(ix, imID, implID, EdgeImpl) {
		t.Errorf("缺少 EdgeImpl: Greeter.Greet(%d) → Hello.Greet(%d)", imID, implID)
	}

	// MyHello 通过嵌入 Hello 提升方法，也应满足 Greeter：验证方法集包含 Greet
	found := false
	for _, mID := range goldenMethodSet(t, ix, "MyHello") {
		if ix.Nodes[mID].Name == "Greet" {
			found = true
		}
	}
	if !found {
		t.Error("MyHello 方法集应包含提升的 Greet")
	}
}

// goldenMethodSet 在 golden 包符号表上计算方法集（复用内部实现验证）。
func goldenMethodSet(t *testing.T, ix *Index, tname string) []int {
	t.Helper()
	ps := &pkgSyms{dir: ".", top: map[string]int{}, methods: map[string][]int{}, byRecv: map[string][]int{}, typeByName: map[string]int{}}
	for i := range ix.Nodes {
		n := &ix.Nodes[i]
		switch n.Kind {
		case KindMethod:
			ps.byRecv[n.Receiver] = append(ps.byRecv[n.Receiver], n.ID)
		case KindStruct, KindInterface, KindType:
			ps.typeByName[n.Name] = n.ID
		}
	}
	return methodSet(ps, ix, tname)
}

func TestImportDir(t *testing.T) {
	cases := []struct {
		importPath, modulePath string
		wantDir                string
		wantOK                 bool
	}{
		{"go-agent/internal/llm", "go-agent", "internal/llm", true},
		{"go-agent", "go-agent", ".", true},
		{"fmt", "go-agent", "", false},            // stdlib
		{"github.com/x/y", "go-agent", "", false}, // 外部依赖
		{"go-agent/zz", "go-agent", "zz", true},
	}
	for _, c := range cases {
		dir, ok := importDir(c.importPath, c.modulePath)
		if dir != c.wantDir || ok != c.wantOK {
			t.Errorf("importDir(%q, %q) = (%q, %v), 期望 (%q, %v)", c.importPath, c.modulePath, dir, ok, c.wantDir, c.wantOK)
		}
	}
}

func TestParseSig(t *testing.T) {
	cases := []struct {
		sig         string
		params, res int
	}{
		{"()", 0, 0},
		{"() string", 0, 1},
		{"(a, b int) int", 2, 1},
		{"(a int) (int, error)", 1, 2},
		{"(s string, args ...any)", 2, 0},
		{"() (n int)", 0, 1},
	}
	for _, c := range cases {
		p, r := parseSig(c.sig)
		if p != c.params || r != c.res {
			t.Errorf("parseSig(%q) = (%d, %d), 期望 (%d, %d)", c.sig, p, r, c.params, c.res)
		}
	}
}
