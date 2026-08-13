// Package kotlin 提供 tree-sitter Kotlin 语法的 Go 绑定。
// 语法源码 vendored 自原版 codegraph 内核（fwcd/tree-sitter-kotlin 0.3.8，sha 校验）。
package kotlin

// #cgo CFLAGS: -std=c11 -fPIC -Wno-unused-parameter -Wno-unused-but-set-variable -Wno-trigraphs
// #include "src/parser.c"
// #include "src/scanner.c"
import "C"

import "unsafe"

// Language 返回 Kotlin 语法的 tree-sitter Language 指针。
func Language() unsafe.Pointer {
	return unsafe.Pointer(C.tree_sitter_kotlin())
}
