// go-agent GUI 版入口：基于 miqt（Qt6 Go 绑定）。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"go-agent/internal/bootstrap"
)

func main() {
	// 注意：flag 必须在 qt.NewQApplication 之前解析，否则未知参数会原样传给 Qt。
	dumpSystemPrompt := flag.Bool("dump-system-prompt", false,
		"启动后将完整 system prompt 写入 "+bootstrap.SystemPromptDumpName+"（调试技能加载/注入用）")
	flag.Parse()

	g := &gui{}

	bootApp, err := bootstrap.Setup(bootstrap.Options{
		OnContent:      g.pushText,
		OnToolStart:    func(name, detail string) { g.blockStart(name, detail) },
		OnToolOutput:   func(line string) { g.cmdLine(line) },
		OnToolEnd:      func(name string) { g.flushCmd() },
		OnTokenCount:   g.pushToken,
		OnUsage:        g.pushUsage,
		OnAskContinue:  g.askContinue,
		AskUser:        g.askUser,
	})
	if err != nil {
		// 装配失败（如未配置 API Key）时，只要显式要求 dump，仍生成调试文件再退出。
		if *dumpSystemPrompt {
			if derr := bootstrap.DumpSystemPromptFallback(); derr != nil {
				fmt.Fprintf(os.Stderr, "写入 system prompt 调试文件失败: %v\n", derr)
			} else {
				fmt.Printf("已输出完整 system prompt 到 %s\n", bootstrap.SystemPromptDumpPath())
			}
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	g.app = bootApp

	// --dump-system-prompt：启动后把完整 system prompt 写入文件，便于调试技能加载与注入。
	if *dumpSystemPrompt {
		if derr := bootApp.DumpSystemPrompt(context.Background()); derr != nil {
			fmt.Fprintf(os.Stderr, "写入 system prompt 调试文件失败: %v\n", derr)
		} else {
			path := bootstrap.SystemPromptDumpPath()
			fmt.Printf("已输出完整 system prompt 到 %s\n", path)
			g.dumpPath = path
		}
	}

	if err := g.run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
