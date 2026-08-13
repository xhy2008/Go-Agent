// ignore.go 目录扫描的忽略规则：默认忽略模式 + .gitignore 解析。
//
// 语义对齐官方 codegraph（colbymchenry/codegraph）src/extraction/index.ts 的
// scanDirectoryWalk：默认忽略清单（DEFAULT_IGNORE_PATTERNS）与根目录 .gitignore
// 合并进同一个匹配器（因此根 .gitignore 的否定规则如 `!vendor/` 可以覆盖默认忽略），
// 子目录的 .gitignore 各自编译成独立匹配器并相对其声明目录求值（同 git 的嵌套
// .gitignore 语义）。目录被忽略时直接不进入（SkipDir），因此“父目录被忽略则子
// 文件无法用 ! 重纳入”的 git 规则天然成立。
package codegraph

import (
	"os"
	"path/filepath"
	"strings"

	gitignore "github.com/sabhiram/go-gitignore"
)

// maxFileSize 对齐原版 MAX_FILE_SIZE：大于 1MB 的源文件跳过
// （生成的 bundle、压缩产物、vendored blob，成本高且无有效符号）。
const maxFileSize = 1024 * 1024

// defaultIgnorePatterns 对齐原版 DEFAULT_IGNORE_PATTERNS（依赖/构建/缓存/工具输出
// 目录，取自 github/gitignore 模板）。未列入的是 IDE/状态目录（.idea/.vs 等），
// 因为只索引受支持的源扩展名，它们不产生符号，无需跳过。
// 注：third_party/ 等项目内的 vendored 源码目录不再硬编码忽略，改由项目 .gitignore
// 治理（索引尊重 .gitignore），保持默认清单与官方一致。
var defaultIgnorePatterns = []string{
	// 依赖目录（JS/TS）
	"node_modules/", "bower_components/", "jspm_packages/", "web_modules/",
	".yarn/", ".pnpm-store/",
	// 框架/打包器构建与缓存（JS/TS）
	".next/", ".nuxt/", ".svelte-kit/", ".turbo/", ".vite/", ".parcel-cache/",
	".angular/", ".docusaurus/", "storybook-static/", ".vinxi/", ".nitro/",
	"out-tsc/", ".vercel/", ".netlify/", ".wrangler/",
	// 构建输出（跨生态）
	"dist/", "build/", "out/", ".output/",
	// 测试/覆盖率
	"coverage/", ".nyc_output/",
	// Python
	"__pycache__/", "__pypackages__/", ".venv/", "venv/", ".pixi/", ".pdm-build/",
	".mypy_cache/", ".pytest_cache/", ".ruff_cache/", ".tox/", ".nox/", ".hypothesis/",
	".ipynb_checkpoints/", ".eggs/",
	// Rust / JVM（Maven、Gradle、Scala）
	"target/", ".gradle/",
	// .NET
	"obj/",
	// vendored 依赖（Go / PHP Composer / Ruby Bundler）
	"vendor/",
	// Swift / iOS
	".build/", "Pods/", "Carthage/", "DerivedData/", ".swiftpm/",
	// Dart / Flutter
	".dart_tool/", ".pub-cache/",
	// Native（Android NDK、C/C++ 依赖）
	".cxx/", ".externalNativeBuild/", "vcpkg_installed/",
	// Scala 工具链
	".bloop/", ".metals/",
	// Lua / Luau（LuaRocks）
	"lua_modules/", ".luarocks/",
	// Delphi / RAD Studio 备份（重复 .pas 源）
	"__history/", "__recovery/",
	// 通用缓存
	".cache/",
	// 附加 glob
	"*.egg-info/",    // Python 打包元数据
	"cmake-build-*/", // CLion / CMake 构建树
	"bazel-*/",       // Bazel 输出符号链接树
	// Android 资源目录（任意层级，含 qualifier 变体：values-es、drawable-hdpi…）
	"**/res/anim*/", "**/res/animator*/", "**/res/color*/", "**/res/drawable*/",
	"**/res/font*/", "**/res/layout*/", "**/res/menu*/", "**/res/mipmap*/",
	"**/res/navigation*/", "**/res/transition*/", "**/res/values*/", "**/res/xml*/",
}

// scopedIgnore 一个 .gitignore 匹配器。dir 为声明该 .gitignore 的目录，
// 模式相对 dir 求值（对齐原版 ScopeIgnore / git 嵌套 .gitignore 语义）。
type scopedIgnore struct {
	dir string
	ig  *gitignore.GitIgnore
}

// isIgnored 判定 fullPath 是否被任一活跃匹配器忽略。目录路径传入时 isDir=true，
// 相对路径补尾部 "/" 使目录限定模式（如 `build/`）生效。
func isIgnored(fullPath string, isDir bool, matchers []scopedIgnore) bool {
	for _, m := range matchers {
		rel, err := filepath.Rel(m.dir, fullPath)
		if err != nil || rel == "." || rel == ".." ||
			strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue // 不在该匹配器声明目录之下
		}
		p := filepath.ToSlash(rel)
		if isDir {
			p += "/"
		}
		if m.ig.MatchesPath(p) {
			return true
		}
	}
	return false
}

// buildDefaultIgnore 构建基础匹配器：默认忽略模式 + 根目录 .gitignore 合并，
// 使根 .gitignore 的否定规则（如 `!vendor/`）可覆盖默认忽略——这是官方文档明确
// 的 opt-in 方式。对齐原版 buildDefaultIgnore。
func buildDefaultIgnore(root string) *gitignore.GitIgnore {
	lines := make([]string, 0, len(defaultIgnorePatterns)+16)
	lines = append(lines, defaultIgnorePatterns...)
	if data, err := os.ReadFile(filepath.Join(root, ".gitignore")); err == nil {
		lines = append(lines, strings.Split(string(data), "\n")...)
	}
	return gitignore.CompileIgnoreLines(lines...)
}

// loadIgnore 加载 dir 目录下的 .gitignore 为独立匹配器；不存在或为空时返回 nil。
// 根目录的 .gitignore 已并入 buildDefaultIgnore，walk 时跳过，避免重复判定。
func loadIgnore(dir string) *scopedIgnore {
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		return nil
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	return &scopedIgnore{dir: dir, ig: gitignore.CompileIgnoreLines(strings.Split(string(data), "\n")...)}
}
