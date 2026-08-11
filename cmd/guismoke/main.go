// 冒烟测试：验证聊天消息列表（气泡 + 命令折叠块）在 AI 流式更新、
// 命令输出追加、以及反复折叠/展开点击下稳定运行、不崩溃。
//
// 复现用户场景：后台 goroutine 模拟 Agent 流式输出与命令输出投递
// （channel + 主线程 QTimer 消费，与 agent-gui 相同的线程模型），
// 同时主线程模拟用户反复点击折叠块标题。全部完成后正常退出。
package main

import (
	"fmt"
	"math/rand"
	"os"
	"time"

	qt "github.com/mappu/miqt/qt6"
)

type block struct {
	header   *qt.QToolButton
	output   *qt.QLabel
	name     string
	content  string
	expanded bool
}

func main() {
	qt.NewQApplication(os.Args)

	win := qt.NewQMainWindow2()
	win.SetWindowTitle("guismoke")
	win.SetMinimumSize2(900, 600)

	central := qt.NewQWidget2()
	root := qt.NewQVBoxLayout2()
	central.SetLayout(root.QLayout)
	win.SetCentralWidget(central)

	scroll := qt.NewQScrollArea2()
	scroll.SetWidgetResizable(true)
	chatBox := qt.NewQWidget2()
	chatLayout := qt.NewQVBoxLayout2()
	chatLayout.SetSpacing(8)
	chatBox.SetLayout(chatLayout.QLayout)
	scroll.SetWidget(chatBox)
	root.AddWidget2(scroll.QWidget, 1)
	bar := scroll.VerticalScrollBar()

	// 命令折叠块 ×10（QFrame + QToolButton 标题 + QLabel 输出，同 agent-gui）。
	var blocks []*block
	for i := 0; i < 10; i++ {
		b := &block{name: fmt.Sprintf("task-%d", i)}
		frame := qt.NewQFrame2()
		fl := qt.NewQVBoxLayout2()
		frame.SetLayout(fl.QLayout)

		btn := qt.NewQToolButton2()
		btn.SetText("▸ 运行命令: " + b.name)
		btn.SetToolButtonStyle(qt.ToolButtonTextOnly)
		btn.OnClicked(func() {
			b.expanded = !b.expanded
			b.output.SetVisible(b.expanded)
			if b.expanded {
				btn.SetText("▾ 运行命令: " + b.name)
			} else {
				btn.SetText("▸ 运行命令: " + b.name)
			}
		})
		fl.AddWidget(btn.QWidget)

		out := qt.NewQLabel2()
		out.SetWordWrap(true)
		out.SetVisible(false)
		fl.AddWidget(out.QWidget)

		chatLayout.AddWidget(frame.QWidget)
		b.header = btn
		b.output = out
		blocks = append(blocks, b)
	}

	// AI 消息气泡（Markdown 渲染 QLabel）。
	aiRow := qt.NewQWidget2()
	aiLayout := qt.NewQHBoxLayout2()
	aiRow.SetLayout(aiLayout.QLayout)
	bubble := qt.NewQFrame2()
	bubble.SetMaximumWidth(720)
	bl := qt.NewQHBoxLayout2()
	bubble.SetLayout(bl.QLayout)
	aiLbl := qt.NewQLabel2()
	aiLbl.SetTextFormat(qt.MarkdownText)
	aiLbl.SetWordWrap(true)
	bl.AddWidget(aiLbl.QWidget)
	aiLayout.AddWidget(bubble.QWidget)
	aiLayout.AddStretch()
	chatLayout.AddWidget(aiRow)

	// 主线程消费队列（模拟 agent-gui 的 updates channel）。
	updates := make(chan func(), 1024)
	post := func(fn func()) {
		select {
		case updates <- fn:
		default:
		}
	}

	// 后台 goroutine：模拟 Agent 流式输出 + 命令输出 + 自动折叠。
	go func() {
		r := rand.New(rand.NewSource(42))
		n := 0
		for n < 600 {
			time.Sleep(15 * time.Millisecond)
			bi := r.Intn(len(blocks))
			b := blocks[bi]
			if r.Intn(10) < 6 {
				line := fmt.Sprintf("[%04d] line %d: value=%.3f 耗时=12ms\n", n, n, r.Float64())
				post(func() {
					b.content += line
					b.output.SetText(b.content)
					b.output.SetVisible(true)
					if !b.expanded {
						b.expanded = true
						b.header.SetText("▾ 运行命令: " + b.name)
					}
					chatLayout.Activate()
					bar.SetValue(bar.Maximum())
				})
			} else {
				md := fmt.Sprintf("## 更新 %d\n\n流式文本 **加粗** `code`\n\n```go\nfunc f() {\n\treturn %d\n}\n```\n\n段落文本 %d。", n, n, n)
				post(func() {
					aiLbl.SetText(md)
					chatLayout.Activate()
					bar.SetValue(bar.Maximum())
				})
			}
			// 1/8 概率自动折叠某个块（模拟命令执行完毕 flushCmd）。
			if r.Intn(8) == 0 {
				b2 := blocks[r.Intn(len(blocks))]
				post(func() {
					b2.expanded = false
					b2.output.SetVisible(false)
					b2.header.SetText("▸ 运行命令: " + b2.name)
					chatLayout.Activate()
					bar.SetValue(bar.Maximum())
				})
			}
			n++
		}
	}()

	// 主线程每 40ms：消费队列 + 模拟用户反复点击折叠块标题。
	var clicks int
	tmr := qt.NewQTimer()
	tmr.SetInterval(40)
	tmr.OnTimeout(func() {
		// 消费后台投递的更新。
		for {
			select {
			case fn := <-updates:
				fn()
				continue
			default:
			}
			break
		}
		// 模拟用户点击：随机切换一个折叠块。
		b := blocks[int(clicks)%len(blocks)]
		b.expanded = !b.expanded
		b.output.SetVisible(b.expanded)
		if b.expanded {
			b.header.SetText("▾ 运行命令: " + b.name)
		} else {
			b.header.SetText("▸ 运行命令: " + b.name)
		}
		clicks++
		chatLayout.Activate()
		bar.SetValue(bar.Maximum())
		if clicks >= 300 {
			tmr.Stop()
			fmt.Println("SMOKE_OK clicks=300")
			qt.QCoreApplication_Quit()
		}
	})
	tmr.Start2()

	win.Show()
	qt.QApplication_Exec()
	fmt.Println("EXIT_NORMAL")
}
