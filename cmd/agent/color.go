package main

import (
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// colorEnabled 表示当前输出是否支持 ANSI 颜色。
var colorEnabled = true

func init() {
	// Windows 控制台默认不解析 ANSI 转义序列，需手动启用 VT 处理
	if runtime.GOOS == "windows" && !enableWindowsVT() {
		colorEnabled = false
	}
	// 输出被重定向到管道/文件时不输出颜色，避免乱码污染重定向内容
	if fi, err := os.Stdout.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		colorEnabled = false
	}
}

// enableWindowsVT 为当前控制台启用 ANSI 转义序列支持。
// 输出非控制台（如被重定向）时返回 false。
func enableWindowsVT() bool {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getMode := kernel32.NewProc("GetConsoleMode")
	setMode := kernel32.NewProc("SetConsoleMode")

	var mode uint32
	if r, _, _ := getMode.Call(os.Stdout.Fd(), uintptr(unsafe.Pointer(&mode))); r == 0 {
		return false
	}
	const enableVirtualTerminalProcessing = 0x0004
	r, _, _ := setMode.Call(os.Stdout.Fd(), uintptr(mode|enableVirtualTerminalProcessing))
	return r != 0
}

// paint 为文本添加颜色；不支持颜色时原样返回。
func paint(text, code string) string {
	if !colorEnabled {
		return text
	}
	return "\033[" + code + "m" + text + "\033[0m"
}
