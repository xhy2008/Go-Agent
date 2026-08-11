// go-agent 是一个极简原生 Coding Agent。
// CLI 入口：启动交互式会话，支持工具调用、联网搜索、技能、记忆与会话恢复。
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"go-agent/internal/bootstrap"
	"go-agent/internal/logx"
	"go-agent/internal/session"
	"go-agent/internal/tools"
)

func main() {
	app, err := bootstrap.Setup(bootstrap.Options{
		OnContent: func(s string) { fmt.Print(s) },
		OnToolStart: func(name, detail string) {
			fmt.Printf("\n%s\n", paint("[工具] 正在调用: "+detail, "33"))
		},
		OnToolOutput: func(line string) {
			fmt.Printf("%s\n", paint("[cmd] "+line, "36"))
		},
		OnTokenCount: func(used, limit int) {
			fmt.Printf("%s\n", paint(fmt.Sprintf("[上下文 %d/%d tokens]", used, limit), "90"))
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer app.Close()

	fmt.Printf("go-agent 启动。模型: %s | 搜索: %s | 技能: %d | 记忆: %v\n",
		app.Client.Model(), app.SearchMgr.BackendName(), len(app.Skills), app.MemLong != nil)
	fmt.Printf("工具: %s\n", toolNames(app.Registry))
	fmt.Println("输入问题开始对话；/help 查看命令；/exit 退出。")
	fmt.Println("工作目录: " + mustCwd())

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for {
		fmt.Print("\n" + paint("你> ", "32"))
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		handled, done := handleCommand(input, app)
		if done {
			return
		}
		if handled {
			continue
		}

		// 每轮注册独立信号监听：Ctrl+C 取消本轮任务，会话继续。
		signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

		_, err := app.Agent.Run(signalCtx, input)
		switch {
		case signalCtx.Err() != nil:
			fmt.Printf("\n%s\n", paint("[已停止]", "31"))
			logx.Warn("本轮任务被用户中断")
		case err != nil:
			fmt.Printf("\n%s\n", paint("[错误] "+err.Error(), "31"))
			logx.Error("任务执行错误: %v", err)
		}
		stop()

		// 自动保存最新会话
		if _, err := session.Save(app.SessionDir, "latest", app.Agent.Messages()); err != nil {
			fmt.Printf("%s\n", paint("[警告] 保存会话失败: "+err.Error(), "31"))
			logx.Warn("自动保存会话失败: %v", err)
		}
	}
}

// handleCommand 处理斜杠命令，返回 (是否已处理, 是否退出)。
func handleCommand(input string, app *bootstrap.App) (handled, quit bool) {
	switch {
	case input == "/exit" || input == "/quit":
		fmt.Println("再见。")
		return true, true
	case input == "/help":
		printHelp()
		return true, false
	case input == "/clear" || input == "/reset":
		app.Agent.Reset()
		tools.ResetModified()
		fmt.Println("会话历史已清空。")
		return true, false
	case input == "/sessions":
		names, err := session.List(app.SessionDir)
		if err != nil {
			logx.Warn("列出会话失败: %v", err)
			fmt.Printf("%s\n", paint("列出会话失败: "+err.Error(), "31"))
			return true, false
		}
		fmt.Println(session.Describe(names))
		return true, false
	case input == "/save":
		if _, err := session.Save(app.SessionDir, "", app.Agent.Messages()); err != nil {
			logx.Warn("保存会话失败: %v", err)
			fmt.Printf("%s\n", paint("保存失败: "+err.Error(), "31"))
		} else {
			fmt.Println("已保存当前会话（名称见 /sessions）。")
		}
		return true, false
	case strings.HasPrefix(input, "/load"):
		name := strings.TrimSpace(strings.TrimPrefix(input, "/load"))
		if name == "" {
			fmt.Println("用法: /load <会话名>（可用 /sessions 查看）")
			return true, false
		}
		msgs, err := session.Load(app.SessionDir, name)
		if err != nil {
			logx.Warn("加载会话 %s 失败: %v", name, err)
			fmt.Printf("%s\n", paint("加载失败: "+err.Error(), "31"))
			return true, false
		}
		app.Agent.SetMessages(msgs)
		fmt.Printf("已恢复会话 %s（%d 条消息）。\n", name, len(msgs))
		return true, false
	case strings.HasPrefix(input, "/"):
		fmt.Printf("未知命令: %s（/help 查看帮助）\n", input)
		return true, false
	default:
		return false, false
	}
}

func toolNames(reg *tools.Registry) string {
	names := make([]string, 0, len(reg.List()))
	for _, t := range reg.List() {
		names = append(names, t.Name())
	}
	return strings.Join(names, ", ")
}

func mustCwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "?"
	}
	return wd
}

func printHelp() {
	fmt.Println(`
可用命令:
  /help       显示本帮助
  /clear      清空会话历史
  /save       保存当前会话（命名）
  /sessions   列出已保存的会话
  /load <名>  恢复指定会话
  /exit       退出程序

使用提示:
  - 输入普通问题，Agent 会自主决定是否调用工具
  - Agent 执行命令时会实时显示 [cmd] 输出
  - 长耗时命令可用 background=true 后台执行，Agent 会轮询状态
  - 每轮结束自动保存到 latest 会话；Ctrl+C 中断当前任务，会话继续`)
}
