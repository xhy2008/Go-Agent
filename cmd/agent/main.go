// go-agent 是一个极简原生 Coding Agent。
// CLI 入口：启动交互式会话，支持工具调用、联网搜索、技能与会话恢复。
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go-agent/internal/bootstrap"
	"go-agent/internal/logx"
	"go-agent/internal/session"
	"go-agent/internal/skill"
	"go-agent/internal/tools"
)

func main() {
	dumpSystemPrompt := flag.Bool("dump-system-prompt", false,
		"启动后将完整 system prompt 写入 "+bootstrap.SystemPromptDumpName+"（调试技能加载/注入用）")
	flag.Parse()

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
		OnUsage: func(hit, miss int) {
			fmt.Printf("%s\n", paint(fmt.Sprintf("[缓存命中 %d / 未命中 %d]", hit, miss), "90"))
		},
		OnAskContinue: func(used int) bool {
			fmt.Printf("%s\n", paint(fmt.Sprintf("[已达到最大工具调用次数 %d，是否继续？(y/N)]", used), "33"))
			var ans string
			fmt.Scanln(&ans)
			ans = strings.ToLower(strings.TrimSpace(ans))
			return ans == "y" || ans == "yes"
		},
		AskUser: func(question string) string {
			fmt.Printf("%s\n", paint("[询问] "+question, "33"))
			var ans string
			fmt.Scanln(&ans)
			return strings.TrimSpace(ans)
		},
	})
	if err != nil {
		// 装配失败（如未配置 API Key）时，只要显式要求 dump，仍生成调试文件再退出。
		if *dumpSystemPrompt {
			if derr := bootstrap.DumpSystemPromptFallback(); derr != nil {
				fmt.Printf("%s\n", paint("写入 system prompt 调试文件失败: "+derr.Error(), "31"))
			} else {
				fmt.Printf("%s\n", paint("已输出完整 system prompt 到 "+bootstrap.SystemPromptDumpPath(), "36"))
			}
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// --dump-system-prompt：启动后把完整 system prompt 写入文件，便于调试技能加载与注入。
	if *dumpSystemPrompt {
		if err := app.DumpSystemPrompt(context.Background()); err != nil {
			fmt.Printf("%s\n", paint("写入 system prompt 失败: "+err.Error(), "31"))
			logx.Warn("写入 system prompt 失败: %v", err)
		} else {
			fmt.Printf("%s\n", paint("已输出完整 system prompt 到 "+bootstrap.SystemPromptDumpPath(), "36"))
		}
	}

	fmt.Printf("go-agent 启动。模型: %s | 搜索: %s | 技能: %d\n",
		app.Client.Model(), app.SearchMgr.BackendName(), len(app.Skills))
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
	case strings.HasPrefix(input, "/skills"):
		// /skills         列出已加载技能
		// /skills reload  重新扫描 .agent/skills/ 并热更新
		if strings.TrimSpace(input) == "/skills reload" {
			sk, err := skill.Load("")
			if err != nil {
				logx.Warn("重新加载技能失败: %v", err)
				fmt.Printf("%s\n", paint("重新加载技能失败: "+err.Error(), "31"))
				return true, false
			}
			app.Skills = sk
			app.Agent.SetSkills(sk)
			fmt.Printf("已重新加载技能：%d 个\n", len(sk))
			return true, false
		}
		listSkills(app.Skills)
		return true, false
	case input == "/codegraph":
		// 手动重建代码图索引（同步等待，展示耗时与结果；每轮任务结束也会自动重建）
		fmt.Println("正在重建代码图索引…")
		start := time.Now()
		ix, err := app.Codegraph.Reindex(app.CodegraphRoot)
		if err != nil {
			logx.Warn("codegraph 索引重建失败: %v", err)
			fmt.Printf("%s\n", paint("[错误] 索引重建失败: "+err.Error(), "31"))
			return true, false
		}
		elapsed := time.Since(start).Round(time.Millisecond)
		fmt.Printf("索引重建完成: %d 符号 / %d 关系（耗时 %v）\n", len(ix.Nodes), len(ix.Edges), elapsed)
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

// listSkills 打印已加载技能（名称、分类、版本）。
func listSkills(skills []*skill.Skill) {
	if len(skills) == 0 {
		fmt.Println("当前未加载任何技能（.agent/skills/ 不存在或无有效技能）。")
		return
	}
	fmt.Printf("已加载技能 %d 个（code 分类常驻，其余用 list_skills 按需查询）：\n", len(skills))
	for _, s := range skills {
		line := "  - " + s.Name
		if s.Category != "" {
			line += " [" + s.Category + "]"
		}
		if s.Version != "" {
			line += " (v" + s.Version + ")"
		}
		fmt.Println(line)
	}
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
  /skills     列出已加载技能
  /skills reload  重新扫描 .agent/skills/ 并热更新（改技能无需重启）
  /codegraph  手动重建代码图索引（默认每轮任务后自动增量重建）
  /exit       退出程序

使用提示:
  - 输入普通问题，Agent 会自主决定是否调用工具
  - Agent 执行命令时会实时显示 [cmd] 输出
  - 长耗时命令可用 background=true 后台执行，Agent 会轮询状态
  - 每轮结束自动保存到 latest 会话；Ctrl+C 中断当前任务，会话继续`)
}
