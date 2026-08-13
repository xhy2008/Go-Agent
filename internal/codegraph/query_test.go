package codegraph

import "testing"

const goldenRoot = "testdata/golden"

func TestExplore_ScoringAndEdges(t *testing.T) {
	ix := buildGolden(t)

	// 精确命中 add（func）
	ms := ix.Explore(goldenRoot, "add", 5)
	if len(ms) == 0 || ms[0].Node.Name != "add" || ms[0].Node.Kind != KindFunc {
		t.Fatalf("查询 add 首条=%+v, 期望 add func", ms[0].Node)
	}
	if ms[0].Score != 110 {
		t.Errorf("add 得分=%d, 期望 110（100 + 10 大小写精确）", ms[0].Score)
	}
	if len(ms[0].Source) == 0 {
		t.Error("add 应返回源码行")
	}
	// 调用者：helper → add
	if !hasCaller(ix, ms[0], "helper") {
		t.Errorf("add 的调用者应包含 helper，实际 %v", ms[0].Callers)
	}
}

func TestExplore_MethodQualified(t *testing.T) {
	ix := buildGolden(t)

	// 限定名 Hello.Greet 应命中 Hello 方法（优于接口方法）
	ms := ix.Explore(goldenRoot, "Hello.Greet", 5)
	if len(ms) == 0 || ms[0].Node.Name != "Greet" || ms[0].Node.Receiver != "Hello" {
		t.Fatalf("查询 Hello.Greet 首条=%+v, 期望 Hello.Greet", ms[0].Node)
	}
	// 方法应带 impl 相关边（实现自 Greeter 接口方法）
	if len(ms[0].Impls) == 0 {
		t.Error("Hello.Greet 应有 EdgeImpl 相关边")
	}
	// 无测试文件 → 无测试覆盖提示
	if len(ms[0].TestRefs) != 0 {
		t.Errorf("golden 无测试文件，TestRefs=%v", ms[0].TestRefs)
	}
}

func TestExplore_InterfaceImpl(t *testing.T) {
	ix := buildGolden(t)

	ms := ix.Explore(goldenRoot, "Greeter.Greet", 5)
	if len(ms) == 0 || ms[0].Node.Receiver != "Greeter" {
		t.Fatalf("查询 Greeter.Greet 首条=%+v", ms[0].Node)
	}
	// 接口方法的实现者：Hello.Greet（impl 出边）
	implOK := false
	for _, e := range ms[0].Impls {
		if e.Kind == EdgeImpl && ix.Nodes[e.To].Receiver == "Hello" {
			implOK = true
		}
	}
	if !implOK {
		t.Errorf("Greeter.Greet 应有到 Hello.Greet 的 impl 边，实际 %v", ms[0].Impls)
	}
}

func TestExplore_BlastRadius(t *testing.T) {
	ix := buildGolden(t)

	// NewHello 被 main 调用；main 还调用 GreetMe → 影响面包含
	ms := ix.Explore(goldenRoot, "NewHello", 5)
	if len(ms) == 0 {
		t.Fatal("未命中 NewHello")
	}
	// NewHello 出边调用：无（内部仅构造字面量）
	// 影响面 BFS：NewHello 无出边调用 → Blast 空
	if len(ms[0].Blast) != 0 {
		t.Errorf("NewHello 出边 BFS 应为空，实际 %v", ms[0].Blast)
	}
	// main 的调用者视角：main 的出边包含 NewHello 与 GreetMe
	msMain := ix.Explore(goldenRoot, "main", 5)
	if len(msMain) == 0 {
		t.Fatal("未命中 main")
	}
	if len(msMain[0].Calls) < 2 {
		t.Errorf("main 出边调用应 ≥2（NewHello/GreetMe），实际 %v", msMain[0].Calls)
	}
	if len(msMain[0].Blast) == 0 {
		t.Error("main 的影响面不应为空")
	}
}

func TestExplore_FileKeyword(t *testing.T) {
	ix := buildGolden(t)

	// "helper.go" 文件查询：全部命中应位于 helper.go
	ms := ix.Explore(goldenRoot, "helper.go", 10)
	if len(ms) == 0 {
		t.Fatal("按文件名查询应命中")
	}
	for _, m := range ms {
		if m.Node.File != "helper.go" {
			t.Errorf("文件查询误命中 %s（file=%s）", m.Node.Name, m.Node.File)
		}
	}
	// 全部命中得分应 ≥ 80（文件全名精确）
	for _, m := range ms {
		if m.Score < 80 {
			t.Errorf("%s 得分=%d, 期望 ≥80", m.Node.Name, m.Score)
		}
	}

	// 无扩展名 "helper"：符号精确命中优先
	ms2 := ix.Explore(goldenRoot, "helper", 5)
	if len(ms2) == 0 || ms2[0].Node.Name != "helper" || ms2[0].Node.Kind != KindFunc {
		t.Fatalf("查询 helper 首条=%+v, 期望 helper func", ms2[0].Node)
	}
	if ms2[0].Score != 110 {
		t.Errorf("helper 得分=%d, 期望 110（100 + 10 大小写精确）", ms2[0].Score)
	}
}

func hasCaller(ix *Index, m Match, name string) bool {
	for _, e := range m.Callers {
		if ix.Nodes[e.From].Name == name {
			return true
		}
	}
	return false
}
