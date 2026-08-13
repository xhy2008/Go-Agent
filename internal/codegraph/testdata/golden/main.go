// Package main 演示 codegraph 解析（golden 测试夹具）。
package main

import (
	"fmt"
	"strings"
)

// Greeter 打招呼的接口。
type Greeter interface {
	// Greet 返回问候语。
	Greet() string
}

// Hello 结构体。
type Hello struct {
	name string
}

// Greet 实现 Greeter 接口。
func (h Hello) Greet() string {
	return "hello " + h.name
}

// NewHello 构造函数。
func NewHello(name string) *Hello {
	return &Hello{name: name}
}

const greeting = "hi"

var count int

// main 入口。
func main() {
	g := NewHello("world")
	_ = g.Greet()
	_ = g.GreetMe()
	fmt.Println(greeting, count)
	_ = strings.ToUpper(greeting)
}
