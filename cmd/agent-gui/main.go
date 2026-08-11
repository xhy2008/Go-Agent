// go-agent GUI 版入口：基于 Cogent Core（纯 Go 原生渲染的轻量 GUI 框架）。
package main

import (
	"fmt"
	"os"

	"go-agent/internal/bootstrap"
)

func main() {
	g := &gui{}

	bootApp, err := bootstrap.Setup(bootstrap.Options{
		OnContent:    g.pushText,
		OnToolStart:  func(name, detail string) { g.blockStart(name, detail) },
		OnToolOutput: func(line string) { g.cmdLine(line) },
		OnToolEnd:    func(name string) { g.flushCmd() },
		OnTokenCount: g.pushToken,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer bootApp.Close()

	g.app = bootApp

	if err := g.run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
