// Package main 与 main.go 同包（golden 测试夹具）。
package main

// Num 数字类型。
type Num int

// add 求和。
func add(a, b int) int { return a + b }

// helper 调用 add。
func helper() int {
	return add(1, 2)
}

// Worker 工作接口。
type Worker interface {
	// Work 干活。
	Work() string
}

// addNum 使用包级常量。
func addNum(n Num) Num {
	return n + Num(count)
}

// GreetMe 打招呼（唯一方法名，用于跨文件方法解析测试）。
func (h Hello) GreetMe() string {
	return h.Greet()
}

// MyHello 嵌入 Hello，方法集合并测试。
type MyHello struct {
	Hello
}
