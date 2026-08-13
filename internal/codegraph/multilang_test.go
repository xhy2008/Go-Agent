package codegraph

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMultiLangE2E 验证多语言混合项目的全链路：扫描 → 解析 → Build → Explore。
func TestMultiLangE2E(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"src/main.ts": `// 入口
import { helper } from './util'

export class Greeter {
  greet(who: string): string {
    return helper(who)
  }
}

function main(): void {
  const g = new Greeter()
  g.greet('world')
}
`,
		"src/util.ts": `// 工具
export function helper(name: string): string {
  return 'hi ' + name
}
`,
		"src/calc.py": `# 计算
class Calculator:
    def add(self, a, b):
        return a + b

def run():
    c = Calculator()
    return c.add(1, 2)
`,
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	relFiles, err := goFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(relFiles) != 3 {
		t.Fatalf("goFiles=%v, 期望 3 个源文件", relFiles)
	}

	fgs := make([]*FileGraph, 0, len(relFiles))
	for _, rel := range relFiles {
		g, err := ParseFile(filepath.Join(root, filepath.FromSlash(rel)), rel)
		if err != nil {
			t.Fatalf("ParseFile(%s): %v", rel, err)
		}
		fgs = append(fgs, g)
	}
	ix, err := Build(root, "", fgs)
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string][]int{}
	for i, n := range ix.Nodes {
		byName[n.Name] = append(byName[n.Name], i)
	}
	// 多语言符号齐全
	for _, want := range []string{"Greeter", "greet", "helper", "main", "Calculator", "add", "run"} {
		if len(byName[want]) == 0 {
			t.Errorf("缺少符号 %q（共 %d 符号）", want, len(ix.Nodes))
		}
	}
	// TS 类方法 receiver 正确
	for _, id := range byName["greet"] {
		if ix.Nodes[id].Receiver != "Greeter" {
			t.Errorf("greet 的 receiver=%q, 期望 Greeter", ix.Nodes[id].Receiver)
		}
	}
	// Python 类方法 receiver 正确
	for _, id := range byName["add"] {
		if ix.Nodes[id].Receiver != "Calculator" {
			t.Errorf("add 的 receiver=%q, 期望 Calculator", ix.Nodes[id].Receiver)
		}
	}
	// Explore 可查到跨语言符号
	ms := ix.Explore(root, "Greeter", 5)
	if len(ms) == 0 {
		t.Error("Explore(Greeter) 无命中")
	}
	ms = ix.Explore(root, "Calculator", 5)
	if len(ms) == 0 {
		t.Error("Explore(Calculator) 无命中")
	}
	t.Logf("多语言索引: %d 符号 / %d 关系", len(ix.Nodes), len(ix.Edges))
}
