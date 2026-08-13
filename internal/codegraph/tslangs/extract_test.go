package tslangs

import (
	"fmt"
	"testing"
)

func TestExtractTS(t *testing.T) {
	src := []byte(`// 入口
import { helper } from './util'

// 定义一个类
export class Greeter {
  name: string

  // 打招呼
  greet(who: string): string {
    return helper(this.name, who)
  }
}

function main(): void {
  const g = new Greeter()
  g.greet('world')
}
`)
	g, err := Extract("test.ts", Language("typescript"), src)
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range g.Nodes {
		fmt.Printf("node[%d] kind=%d line=%d name=%q recv=%q doc=%q sig=%q\n",
			i, n.Kind, n.Line, n.Name, n.Receiver, n.Doc, n.Signature)
	}
	for _, r := range g.Refs {
		fmt.Printf("ref  kind=%d name=%q line=%d owner=%d\n", r.Kind, r.Name, r.Line, r.Owner)
	}
	if len(g.Nodes) == 0 {
		t.Fatal("no nodes extracted")
	}
	// Greeter 类 + greet 方法 + main 函数 + name 属性（variable_declarator）
	byName := map[string]bool{}
	for _, n := range g.Nodes {
		byName[n.Name] = true
	}
	for _, want := range []string{"Greeter", "greet", "main"} {
		if !byName[want] {
			t.Errorf("missing symbol %q (have %v)", want, byName)
		}
	}
}

func TestExtractMulti(t *testing.T) {
	cases := []struct {
		file string
		lang string
		src  string
		want []string
	}{
		{"a.py", "python", "class Greeter:\n    def greet(self, who):\n        return helper(self.name, who)\n", []string{"Greeter", "greet"}},
		{"a.java", "java", "class Greeter {\n    String greet(String who) { return helper(this.name, who); }\n}\n", []string{"Greeter", "greet"}},
		{"a.rs", "rust", "struct Greeter {}\nimpl Greeter {\n    fn greet(&self, who: &str) -> String { helper(self.name, who) }\n}\n", []string{"Greeter", "greet"}},
		{"a.cpp", "cpp", "class Greeter {\npublic:\n    std::string greet(const char* who) { return helper(this->name, who); }\n};\n", []string{"Greeter", "greet"}},
		{"a.cs", "csharp", "class Greeter {\n    string Greet(string who) { return Helper(this.name, who); }\n}\n", []string{"Greeter", "Greet"}},
		{"a.rb", "ruby", "class Greeter\n  def greet(who)\n    helper(@name, who)\n  end\nend\n", []string{"Greeter", "greet"}},
		{"a.php", "php", "<?php class Greeter { function greet($who) { return helper($this->name, $who); } } ?>", []string{"Greeter", "greet"}},
		{"a.kt", "kotlin", "class Greeter {\n    fun greet(who: String): String { return helper(this.name, who) }\n}\n", []string{"Greeter", "greet"}},
		{"a.scala", "scala", "class Greeter {\n  def greet(who: String): String = helper(this.name, who)\n}\n", []string{"Greeter", "greet"}},
		{"a.dart", "dart", "class Greeter {\n  String greet(String who) => helper(this.name, who);\n}\n", []string{"Greeter", "greet"}},
	}
	for _, c := range cases {
		g, err := Extract(c.file, Language(c.lang), []byte(c.src))
		if err != nil {
			t.Errorf("%s: %v", c.file, err)
			continue
		}
		byName := map[string]bool{}
		for _, n := range g.Nodes {
			byName[n.Name] = true
		}
		for _, want := range c.want {
			if !byName[want] {
				t.Errorf("%s: missing symbol %q (have %v)", c.file, want, byName)
			}
		}
		t.Logf("%s: %d nodes, %d refs", c.file, len(g.Nodes), len(g.Refs))
	}
}
