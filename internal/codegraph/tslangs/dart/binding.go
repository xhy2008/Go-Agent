// Package dart 提供 tree-sitter Dart 语法的 Go 绑定。
// 语法源码 vendored 自原版 codegraph 内核（UserNobody14/tree-sitter-dart master@d4d8f3e，sha 校验）。
package dart

// #cgo CFLAGS: -std=c11 -fPIC -Wno-unused-parameter -Wno-unused-but-set-variable -Wno-trigraphs -Wno-unused-function
// #include "src/parser.c"
// #include "src/scanner.c"
import "C"

import "unsafe"

// Language 返回 Dart 语法的 tree-sitter Language 指针。
func Language() unsafe.Pointer {
	return unsafe.Pointer(C.tree_sitter_dart())
}
