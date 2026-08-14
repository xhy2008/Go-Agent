package codegraph

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// writeTree 在 base 下写入给定相对路径的文件（自动创建父目录）。
func writeTree(t *testing.T, base string, files ...string) {
	t.Helper()
	for _, f := range files {
		full := filepath.Join(base, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", f, err)
		}
		if err := os.WriteFile(full, []byte("// test\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
}

func setFile(t *testing.T, base, rel string, data []byte) {
	t.Helper()
	full := filepath.Join(base, filepath.FromSlash(rel))
	if err := os.WriteFile(full, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// filesOf 返回 goFiles 收集结果，转成可比较的 map。
func filesOf(t *testing.T, root string) map[string]bool {
	t.Helper()
	files, err := goFiles(root)
	if err != nil {
		t.Fatalf("goFiles: %v", err)
	}
	m := make(map[string]bool, len(files))
	for _, f := range files {
		m[f] = true
	}
	return m
}

func assertHas(t *testing.T, m map[string]bool, rel string) {
	t.Helper()
	if !m[rel] {
		t.Errorf("期望收录 %s，实际: %v", rel, sortedKeys(m))
	}
}

func assertNotHas(t *testing.T, m map[string]bool, rel string) {
	t.Helper()
	if m[rel] {
		t.Errorf("期望跳过 %s，实际: %v", rel, sortedKeys(m))
	}
}

func sortedKeys(m map[string]bool) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// TestDefaultIgnoreDirs 默认忽略清单：依赖/构建/缓存目录被跳过。
func TestDefaultIgnoreDirs(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root,
		"main.go",
		"pkg/util.go",
		"node_modules/pkg/index.js",
		"dist/bundle.js",
		"build/out.go",
		"out/gen.go",
		"target/debug.rs",
		".venv/lib.py",
		"vendor/dep.go",
		"third_party/llama.cpp/foo.c",
		"Pods/Some/pod.swift",
		"__pycache__/m.py",
		"coverage/cov.go",
		".next/build.js",
		".cache/foo.go",
		".idea/foo.go", // 非默认清单的隐藏目录：应被收录（对齐原版）
		"src/app.ts",
		"src/res/values/strings.py",
	)
	m := filesOf(t, root)
	assertHas(t, m, "main.go")
	assertHas(t, m, "pkg/util.go")
	assertHas(t, m, "src/app.ts")
	assertHas(t, m, ".idea/foo.go")
	for _, skipped := range []string{
		"node_modules/pkg/index.js", "dist/bundle.js", "build/out.go", "out/gen.go",
		"target/debug.rs", ".venv/lib.py", "vendor/dep.go",
		"Pods/Some/pod.swift",
		"__pycache__/m.py", "coverage/cov.go", ".next/build.js", ".cache/foo.go",
		"src/res/values/strings.py",
	} {
		assertNotHas(t, m, skipped)
	}
}

// TestThirdPartyNotDefaultIgnored third_party/ 不再硬编码忽略（对齐原版默认清单），
// 由项目 .gitignore 治理；无 .gitignore 时收录，有 .gitignore 忽略时跳过。
func TestThirdPartyNotDefaultIgnored(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "main.go", "third_party/llama.cpp/foo.c", "third_party/util/bar.go")
	// 无 .gitignore：third_party 内容被收录（与原版一致）
	m := filesOf(t, root)
	assertHas(t, m, "third_party/llama.cpp/foo.c")
	assertHas(t, m, "third_party/util/bar.go")

	// 项目 .gitignore 忽略 third_party/：跳过（本项目实际配置）
	root2 := t.TempDir()
	writeTree(t, root2, "main.go", "third_party/llama.cpp/foo.c", "third_party/util/bar.go")
	setFile(t, root2, ".gitignore", []byte("third_party/\n"))
	m2 := filesOf(t, root2)
	assertHas(t, m2, "main.go")
	assertNotHas(t, m2, "third_party/llama.cpp/foo.c")
	assertNotHas(t, m2, "third_party/util/bar.go")
}

// TestRootGitignoreMerge 根 .gitignore 生效，且其否定规则可覆盖默认忽略。
func TestRootGitignoreMerge(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root,
		"main.go",
		"models/embed.py",
		"vendor/keep.go",
		"vendor/skip.go",
	)
	// 根 .gitignore：忽略 models/，用 ! 否定默认忽略的 vendor/（官方 opt-in 方式）
	setFile(t, root, ".gitignore", []byte("models/\n!vendor/\n"))
	m := filesOf(t, root)
	assertHas(t, m, "main.go")
	assertHas(t, m, "vendor/keep.go") // !vendor/ 覆盖默认忽略
	assertNotHas(t, m, "models/embed.py")
	_ = m["vendor/skip.go"]
}

// TestNestedGitignore 嵌套 .gitignore：子目录规则相对其声明目录生效。
func TestNestedGitignore(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root,
		"main.go",
		"a/one.go",
		"a/skip/two.go",
		"b/skip/three.go", // 根级无规则，b/skip 应被收录
	)
	setFile(t, root, "a/.gitignore", []byte("skip/\n"))
	m := filesOf(t, root)
	assertHas(t, m, "main.go")
	assertHas(t, m, "a/one.go")
	assertNotHas(t, m, "a/skip/two.go")
	assertHas(t, m, "b/skip/three.go")
}

// TestGitignorePatterns 常见 gitignore 模式语义。
func TestGitignorePatterns(t *testing.T) {
	cases := []struct {
		name    string
		gi      string
		rel     string
		isDir   bool
		ignored bool
	}{
		{"目录模式 build/", "build/\n", "build", true, true},
		{"目录模式不影响同名文件", "build/\n", "build", false, false},
		{"目录模式覆盖子树文件", "build/\n", "build/x.go", false, true},
		{"锚定 /foo 仅根", "/foo.log\n", "foo.log", false, true},
		{"锚定不影响子目录", "/foo.log\n", "a/foo.log", false, false},
		{"任意层级 foo.log", "foo.log\n", "a/b/foo.log", false, true},
		// 已知差异：sabhiram/go-gitignore 将 `?` 当字面量而非单字符通配符
		// （git/node-ignore 支持）。真实 .gitignore 中 `?` 罕见，记录为差异。
		{"? 单字符（差异：库按字面量处理）", "?.log\n", "a.log", false, false},
		{"* 跨段内", "*.min.js\n", "bundle.min.js", false, true},
		{"! 否定", "*.log\n!keep.log\n", "keep.log", false, false},
		{"! 否定普通忽略", "*.log\n!keep.log\n", "drop.log", false, true},
		{"** 多级", "a/**/z.go\n", "a/b/c/z.go", false, true},
		{"注释与空行", "# 注释\n\n*.log\n", "x.log", false, true},
		{"转义 # 文件", "\\#foo.go\n", "#foo.go", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			writeTree(t, root, "probe.go")
			setFile(t, root, ".gitignore", []byte(c.gi))
			ig := buildDefaultIgnore(root)
			// 只用根 .gitignore（无默认模式干扰）：直接构造 matcher 验证
			gi := &scopedIgnore{dir: root, ig: ig}
			full := filepath.Join(root, filepath.FromSlash(c.rel))
			got := isIgnored(full, c.isDir, []scopedIgnore{*gi})
			if got != c.ignored {
				t.Errorf("isIgnored(%s, isDir=%v) = %v, want %v", c.rel, c.isDir, got, c.ignored)
			}
		})
	}
}

// TestParentDirRule 父目录被忽略时，子文件无法用 ! 重纳入（git 规则）。
func TestParentDirRule(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "main.go", "dist/keep.go", "dist/nested.go")
	// 默认忽略 dist/，根 .gitignore 尝试 !dist/keep.go —— 因父目录 dist/ 仍被忽略，
	// walk 不会进入 dist/，keep.go 无法重纳（对齐 git 与官方）。
	setFile(t, root, ".gitignore", []byte("!dist/keep.go\n"))
	m := filesOf(t, root)
	assertHas(t, m, "main.go")
	assertNotHas(t, m, "dist/keep.go")
	assertNotHas(t, m, "dist/nested.go")
}

// TestParentDirNegationOptIn 用 !dist/ 重纳整个默认忽略目录（官方 opt-in）。
func TestParentDirNegationOptIn(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "main.go", "dist/keep.go")
	setFile(t, root, ".gitignore", []byte("!dist/\n"))
	m := filesOf(t, root)
	assertHas(t, m, "dist/keep.go")
}

// TestMaxFileSize >1MB 文件跳过（对齐原版 MAX_FILE_SIZE）。
func TestMaxFileSize(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "main.go", "big/embed.go")
	setFile(t, root, "big/embed.go", make([]byte, maxFileSize+1))
	m := filesOf(t, root)
	assertHas(t, m, "main.go")
	assertNotHas(t, m, "big/embed.go")
}

// TestGoFilesIntegration 综合：默认忽略 + .gitignore + 1MB + 数据目录。
func TestGoFilesIntegration(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root,
		"main.go",
		"cmd/app/main.go",
		".go-agent/data.go",      // 本应用数据目录，始终跳过
		".go-agent-other/x.go",   // 本应用数据目录变体
		".codegraph/off.go",      // 官方 codegraph 数据目录，始终跳过
		".codegraph-extra/y.go",  // 官方 codegraph 数据目录变体
		".git/hook.py",            // git 内部
		"build/out.go",            // 默认忽略
		"src/secret/generated.go", // 根 .gitignore
		"src/secret/real.go",      // ! 重纳的特定文件
	)
	setFile(t, root, ".gitignore", []byte("src/secret/\n!src/secret/real.go\n"))
	m := filesOf(t, root)
	assertHas(t, m, "main.go")
	assertHas(t, m, "cmd/app/main.go")
	assertNotHas(t, m, ".go-agent/data.go")
	assertNotHas(t, m, ".go-agent-other/x.go")
	assertNotHas(t, m, ".codegraph/off.go")
	assertNotHas(t, m, ".codegraph-extra/y.go")
	assertNotHas(t, m, ".git/hook.py")
	assertNotHas(t, m, "build/out.go")
	// 父目录 src/secret/ 被忽略 → 子文件无法用 ! 重纳（git 规则，见 TestParentDirRule）
	assertNotHas(t, m, "src/secret/generated.go")
	assertNotHas(t, m, "src/secret/real.go")
}

// TestCRLF gitignore 含 \r\n（Windows）正常解析。
func TestCRLF(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "main.go", "tmp/one.go")
	setFile(t, root, ".gitignore", []byte("tmp/\r\n"))
	m := filesOf(t, root)
	assertHas(t, m, "main.go")
	assertNotHas(t, m, "tmp/one.go")
}

// TestDumpGoFiles 比对辅助：设置 GO_AGENT_DUMP_ROOT 后输出移植版 goFiles 文件列表
// （每行一个，排序），供与原版 scanDirectoryWalk（compare-scan.mjs）对比。常规测试跳过。
func TestDumpGoFiles(t *testing.T) {
	root := os.Getenv("GO_AGENT_DUMP_ROOT")
	if root == "" {
		t.Skip("GO_AGENT_DUMP_ROOT 未设置")
	}
	files, err := goFiles(root)
	if err != nil {
		t.Fatalf("goFiles: %v", err)
	}
	if out := os.Getenv("GO_AGENT_DUMP_OUT"); out != "" {
		if werr := os.WriteFile(out, []byte(strings.Join(files, "\n")+"\n"), 0o644); werr != nil {
			t.Fatalf("write dump: %v", werr)
		}
	}
	t.Logf("goFiles: %d files", len(files))
}
func TestDirtyGitignore(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "main.go", "tmp/one.go")
	// 含非法字节（非 UTF-8）：整体跳过该 .gitignore
	_ = os.WriteFile(filepath.Join(root, ".gitignore"), []byte{0xff, 0xfe, 0x00}, 0o644)
	m := filesOf(t, root)
	assertHas(t, m, "main.go")
	assertHas(t, m, "tmp/one.go")
}
