// go-agent GUI 版：基于 miqt（Qt6 Go 绑定，CGO 直调 Qt，无浏览器内核）。
//
// 布局：QMainWindow = 顶部栏（标题+模型/搜索/技能/上下文/状态）
// + 中部（左侧聊天消息列表 + 右侧修改文件 QListWidget）
// + 底部输入栏（QLineEdit + 发送/停止按钮）。
//
// 聊天区为消息列表（QScrollArea + 纵向布局）：用户/AI 消息为气泡，
// 命令输出为可点击折叠块。气泡与命令输出均使用 QLabel（WordWrap 高度
// 自适应，经广泛验证稳定），避免 QTextEdit 的 AdjustToContents 在频繁
// 折叠/展开场景下触发布局重算导致未响应/崩溃。
//
// 线程模型：全部 UI 操作经 updates channel 投递到主线程，由 QTimer
// 消费执行；后台 Agent goroutine 只调用带锁的投递函数，绝不直接操作
// Qt 控件。所有共享字段（aiMarkdown/aiFlushPending/curBlock）用互斥锁
// 保护，aiEdit/aiRow 仅由主线程读写。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	qt "github.com/mappu/miqt/qt6"

	"go-agent/internal/bootstrap"
	"go-agent/internal/llm"
	"go-agent/internal/logx"
	"go-agent/internal/session"
	"go-agent/internal/tools"
)

// panelHeaderStyle 侧栏面板展开时的标题按钮样式。
const panelHeaderStyle = "QToolButton { border:none; color:#9da5b4; font-weight:bold; padding:4px 8px; text-align:left; background:#1f2428; border-bottom:1px solid #3a3f4b; } QToolButton:hover { color:#c9d1d9; }"

// panelClosedStyle 侧栏面板收起时的竖直窄条按钮样式（箭头居中，整条可点击）。
const panelClosedStyle = "QToolButton { border:none; color:#9da5b4; background:#1f2428; font-size:14px; } QToolButton:hover { color:#c9d1d9; background:#2d333b; }"

// 侧栏面板宽度：收起时收窄为竖直窄条（仅显示居中箭头，整条可点击展开）；
// 展开时宽度可在 min~max 范围内拖拽调整（配合 QSplitter）。
const (
	panelClosedWidth = 28

	sessionPanelMinWidth = 160
	sessionPanelMaxWidth = 500
	filePanelMinWidth    = 180
	filePanelMaxWidth    = 600
)

// cmdBlock 是一个命令执行折叠块（标题可点击折叠/展开输出）。
type cmdBlock struct {
	box      *qt.QFrame
	header   *qt.QToolButton
	output   *qt.QTextEdit
	detail   string // 标题显示的摘要（exec_command 显示实际命令，其余显示工具名）
	content  string // 已追加的输出全文
	expanded bool
}

// bubble 是圆角消息气泡：外层 QFrame 绘制圆角背景，内层 QTextEdit 用透明背景承载文本。
// 直接给 QTextEdit 设背景 + border-radius 时，其方形 viewport 会自绘底色盖住圆角背景，
// 四角变尖；拆成 frame（圆角背景）+ edit（透明文本）两层，圆角即可稳定显示。
type bubble struct {
	box *qt.QFrame    // 圆角背景容器（行布局中拉伸，最大宽度 = 气泡最大宽度）
	ed  *qt.QTextEdit // 透明背景文本区（宽度跟随 box，高度由 fixBubbleHeight 固定）
}

// gui 封装 Qt6 界面与 Agent 之间的交互。
type gui struct {
	app *bootstrap.App

	win        *qt.QMainWindow
	chatScroll *qt.QScrollArea
	chatBox    *qt.QWidget
	chatLayout *qt.QVBoxLayout
	chatBar    *qt.QScrollBar // 缓存滚动条引用（避免频繁包装对象）
	bubbles    []*bubble      // 已渲染的消息气泡（宽度/高度随聊天区变化同步）

	input    *qt.QLineEdit
	sendBtn  *qt.QPushButton
	stopBtn  *qt.QPushButton
	sideList *qt.QListWidget // 右侧修改文件列表

	sessionList   *qt.QListWidget // 左侧会话列表
	sessionToggle *qt.QToolButton
	fileToggle    *qt.QToolButton
	sessionPanel  *qt.QWidget // 左侧会话面板（收起时收窄宽度）
	filePanel     *qt.QWidget // 右侧文件面板（收起时收窄宽度）

	splitter *qt.QSplitter // 三栏分割器（收起/展开时显式重排宽度）

	sessionPanelWidth int // 左侧面板展开时的宽度（收起前记住，展开时恢复）
	filePanelWidth    int // 右侧面板展开时的宽度

	statusLabel *qt.QLabel
	tokenLabel  *qt.QLabel
	modelLabel  *qt.QLabel
	searchLabel *qt.QLabel
	skillLabel  *qt.QLabel
	cgBtn       *qt.QPushButton // 顶部"重建索引"按钮（手动重建代码图）
	cgRebuild   bool            // 重建进行中（主线程读写，防重复触发）

	dumpPath string // --dump-system-prompt 输出的调试文件路径（为空表示未启用）

	// updates 是主线程消费的 UI 更新命令队列（后台 goroutine 投递）。
	updates chan func()
	timer   *qt.QTimer // 50ms 消费 updates
	sideTmr *qt.QTimer // 2s 刷新侧边栏

	// 当前 AI 回复的消息气泡（流式累积，节流渲染；仅主线程访问）。
	aiRow          *qt.QWidget
	aiEdit         *qt.QTextEdit
	aiBubble       *bubble // 当前 AI 气泡（box+edit），流式渲染时同步高度
	aiMarkdown     string
	aiFlushPending bool

	curBlock *cmdBlock // 当前正在执行的命令折叠块

	cmdBlocks []*cmdBlock // 全部命令折叠块（resize 时同步标题截断宽度）

	mu      sync.Mutex
	cancel  context.CancelFunc
	running bool
}

// run 创建 Qt 应用、构建界面并进入事件循环。
func (g *gui) run() error {
	g.updates = make(chan func(), 512)

	qt.NewQApplication(os.Args)

	g.buildUI()
	g.setInitialInfo()
	if g.dumpPath != "" {
		g.setStatus("已输出 system prompt 到 " + g.dumpPath)
	}

	// 主线程定时消费 UI 更新（后台 goroutine 只投递函数到 channel）。
	g.timer = qt.NewQTimer()
	g.timer.SetInterval(50)
	g.timer.OnTimeout(g.flushUpdates)
	g.timer.Start2()

	// 侧边栏周期刷新。
	g.sideTmr = qt.NewQTimer()
	g.sideTmr.SetInterval(2000)
	g.sideTmr.OnTimeout(g.refreshSidebar)
	g.sideTmr.Start2()

	// 全局日志监听：错误/警告实时反映到状态栏。
	logx.SetListener(func(level, msg string) {
		switch level {
		case "error":
			g.setStatus("[错误] " + msg)
		case "warn":
			g.setStatus("[警告] " + msg)
		}
	})

	g.win.Show()
	g.relayoutPanels() // 让两侧栏默认停在最小宽度，聊天区（及气泡）更宽
	g.refreshSessions()
	// 会话加载延迟到事件循环首帧：Qt 的布局是异步排队的，Show 返回时聊天区尚未完成
	// 首次布局（chatBox 宽度仍为 0），此时渲染气泡会按宽度下限创建，随后被 resize
	// 回调拉宽，用户会看到"从窄快速扩展到最宽"的抖动。
	rt := qt.NewQTimer()
	rt.SetSingleShot(true)
	rt.SetInterval(0)
	rt.OnTimeout(g.restoreLatest)
	rt.Start2()
	qt.QApplication_Exec()
	return nil
}

// buildUI 构建主窗口布局。
func (g *gui) buildUI() {
	g.win = qt.NewQMainWindow2()
	g.win.SetWindowTitle("go-agent")
	g.win.SetMinimumSize2(1024, 720)

	central := qt.NewQWidget2()
	rootLayout := qt.NewQVBoxLayout2()
	rootLayout.SetSpacing(0)
	central.SetLayout(rootLayout.QLayout)
	g.win.SetCentralWidget(central)

	// 顶部栏：标题 + 信息标签。
	topBar := qt.NewQWidget2()
	topLayout := qt.NewQHBoxLayout(topBar)
	title := qt.NewQLabel3("go-agent")
	title.SetStyleSheet("font-weight:bold; font-size:15px;")
	topLayout.AddWidget(title.QWidget)

	g.modelLabel = g.infoLabel(topLayout, "模型: -")
	g.searchLabel = g.infoLabel(topLayout, "搜索: -")
	g.skillLabel = g.infoLabel(topLayout, "技能: -")
	g.tokenLabel = g.infoLabel(topLayout, "上下文: -")
	g.statusLabel = g.infoLabel(topLayout, "就绪")
	topLayout.AddStretch()
	// 手动重建代码图索引按钮（点击后在后台重建，不阻塞 UI）。
	g.cgBtn = qt.NewQPushButton3("重建索引")
	g.cgBtn.SetToolTip("手动重建代码图索引（任务完成后也会自动增量重建）")
	g.cgBtn.OnClicked(func() { g.rebuildIndex() })
	topLayout.AddWidget(g.cgBtn.QWidget)
	rootLayout.AddWidget(topBar)

	// 中部：左侧会话列表 + 聊天消息列表 + 右侧修改文件列表。
	mid := qt.NewQWidget2()
	midLayout := qt.NewQHBoxLayout(mid)
	midLayout.SetSpacing(0)

	// 左侧：可收起的会话列表面板。
	sessionPanel := qt.NewQWidget2()
	sessionPanelLayout := qt.NewQVBoxLayout2()
	sessionPanelLayout.SetSpacing(0)
	sessionPanel.SetLayout(sessionPanelLayout.QLayout)
	sessionPanel.SetMinimumWidth(sessionPanelMinWidth)
	sessionPanel.SetMaximumWidth(sessionPanelMaxWidth)
	g.sessionPanel = sessionPanel
	g.sessionToggle = qt.NewQToolButton2()
	g.sessionToggle.SetText("▾ 会话")
	g.sessionToggle.SetToolButtonStyle(qt.ToolButtonTextOnly)
	g.sessionToggle.SetStyleSheet(panelHeaderStyle)
	g.sessionToggle.OnClicked(func() { g.toggleSessionPanel() })
	sessionPanelLayout.AddWidget(g.sessionToggle.QWidget)
	g.sessionList = qt.NewQListWidget(sessionPanel)
	g.sessionList.SetStyleSheet("QListWidget { border:none; background:#161b22; color:#c9d1d9; }")
	g.sessionList.OnItemClicked(func(item *qt.QListWidgetItem) { g.loadSession(item.Text()) })
	sessionPanelLayout.AddWidget(g.sessionList.QWidget)

	g.chatScroll = qt.NewQScrollArea2()
	g.chatScroll.SetWidgetResizable(true)
	g.chatBox = qt.NewQWidget2()
	g.chatLayout = qt.NewQVBoxLayout2()
	g.chatLayout.SetSpacing(4) // 消息/命令块之间紧凑排列
	g.chatLayout.SetContentsMargins(12, 6, 12, 6)
	g.chatBox.SetLayout(g.chatLayout.QLayout)
	g.chatScroll.SetWidget(g.chatBox)
	g.chatBar = g.chatScroll.VerticalScrollBar()

	// 聊天区宽度变化（窗口缩放、侧栏收起/展开）时同步气泡最大宽度。
	// 同时挂 chatBox 的 resize：chatBox 跟随 chatScroll viewport 变化，比
	// chatScroll.OnResizeEvent 更稳定（viewport 在父布局调整后会立即 resize）。
	g.chatScroll.OnResizeEvent(func(super func(event *qt.QResizeEvent), event *qt.QResizeEvent) {
		super(event)
		g.updateBubbleWidths()
	})
	g.chatBox.OnResizeEvent(func(super func(event *qt.QResizeEvent), event *qt.QResizeEvent) {
		super(event)
		g.updateBubbleWidths()
	})

	// 右侧：可收起的修改文件列表面板。
	filePanel := qt.NewQWidget2()
	filePanelLayout := qt.NewQVBoxLayout2()
	filePanelLayout.SetSpacing(0)
	filePanel.SetLayout(filePanelLayout.QLayout)
	filePanel.SetMinimumWidth(filePanelMinWidth)
	filePanel.SetMaximumWidth(filePanelMaxWidth)
	g.filePanel = filePanel
	g.fileToggle = qt.NewQToolButton2()
	g.fileToggle.SetText("▾ 修改文件")
	g.fileToggle.SetToolButtonStyle(qt.ToolButtonTextOnly)
	g.fileToggle.SetStyleSheet(panelHeaderStyle)
	g.fileToggle.OnClicked(func() { g.toggleFilePanel() })
	filePanelLayout.AddWidget(g.fileToggle.QWidget)
	g.sideList = qt.NewQListWidget(filePanel)
	g.sideList.SetStyleSheet("QListWidget { border:none; background:#161b22; color:#c9d1d9; }")
	filePanelLayout.AddWidget(g.sideList.QWidget)

	// QSplitter 分隔三栏：左右面板可拖拽调整宽度，聊天区占据剩余空间。
	splitter := qt.NewQSplitter2()
	g.splitter = splitter
	splitter.SetChildrenCollapsible(false)
	splitter.SetHandleWidth(4)
	splitter.AddWidget(sessionPanel)
	splitter.AddWidget(g.chatScroll.QWidget)
	splitter.AddWidget(filePanel)
	splitter.SetStretchFactor(0, 0)
	splitter.SetStretchFactor(1, 1)
	splitter.SetStretchFactor(2, 0)
	// 拖拽分割条调整边栏宽度时，同步气泡最大宽度。
	splitter.OnSplitterMoved(func(pos, index int) {
		g.updateBubbleWidths()
	})
	midLayout.AddWidget(splitter.QWidget)
	rootLayout.AddWidget2(mid, 1)

	// 底部输入栏。
	bottomBar := qt.NewQWidget2()
	bottomLayout := qt.NewQHBoxLayout(bottomBar)
	g.input = qt.NewQLineEdit2()
	g.input.SetPlaceholderText("输入任务，Enter 发送（运行期间输入被忽略）")
	g.input.OnReturnPressed(func() { g.onSend() })
	bottomLayout.AddWidget2(g.input.QWidget, 1)
	g.sendBtn = qt.NewQPushButton3("发送")
	g.sendBtn.OnClicked(func() { g.onSend() })
	bottomLayout.AddWidget(g.sendBtn.QWidget)
	g.stopBtn = qt.NewQPushButton3("停止")
	g.stopBtn.OnClicked(func() { g.onStop() })
	bottomLayout.AddWidget(g.stopBtn.QWidget)
	rootLayout.AddWidget(bottomBar)
}

// infoLabel 创建顶部栏信息标签。
func (g *gui) infoLabel(layout *qt.QHBoxLayout, text string) *qt.QLabel {
	lbl := qt.NewQLabel3(text)
	lbl.SetStyleSheet("color:#9da5b4;")
	layout.AddWidget(lbl.QWidget)
	return lbl
}

// setInitialInfo 填充顶部栏初始信息（主线程）。
func (g *gui) setInitialInfo() {
	g.modelLabel.SetText("模型: " + g.app.Client.Model())
	g.searchLabel.SetText("搜索: " + g.app.SearchMgr.BackendName())
	g.skillLabel.SetText(fmt.Sprintf("技能: %d", len(g.app.Skills)))
}

// ---- 主线程 UI 更新（后台 goroutine 不得直接调用 Qt 控件） ----

// post 将 UI 更新函数投递到主线程（缓冲满则丢弃，避免阻塞后台）。
func (g *gui) post(fn func()) {
	select {
	case g.updates <- fn:
	default:
	}
}

// flushUpdates 由 QTimer 在主线程周期调用，消费 updates 队列。
func (g *gui) flushUpdates() {
	for {
		select {
		case fn := <-g.updates:
			fn()
		default:
			return
		}
	}
}

// scrollToBottom 将聊天区滚动到底部（须在主线程执行）。
func (g *gui) scrollToBottom() {
	if g.chatBar == nil {
		return
	}
	g.chatLayout.Activate() // 立即重新布局，确保 Maximum 反映最新内容高度
	g.chatBar.SetValue(g.chatBar.Maximum())
}

// bubbleMaxWidth 计算聊天气泡的最大水平宽度：聊天区可视宽度的 75%（下限 160px）。
// 宽度源必须限定在可视区域（viewport）内：
// chatBox 位于 widgetResizable 的 QScrollArea 中，当内容 sizeHint 超过视口时会被
// 撑大到 sizeHint 宽度（远超视口），若直接用 chatBox.Width() 计算气泡最大宽度，
// 会形成"气泡变宽 → chatBox sizeHint 变大 → chatBox 变宽 → 气泡再变宽"的正反馈
// 无限增长环（用户会看到气泡持续扩展到最宽）。因此先取 chatBox 宽度，再钳制到
// 视口宽度，确保宽度源稳定。
func (g *gui) bubbleMaxWidth() int {
	w := g.chatBox.Width()
	if w <= 0 {
		// chatBox 尚未布局时退回 chatScroll 视口宽度，仍无效则用 0 触发下限。
		if g.chatScroll != nil {
			w = g.chatScroll.Viewport().Width()
		}
	}
	if g.chatScroll != nil {
		if vw := g.chatScroll.Viewport().Width(); vw > 0 && w > vw {
			w = vw
		}
	}
	max := int(float64(w) * 0.75)
	if max < 160 {
		max = 160
	}
	return max
}

// updateBubbleWidths 将已渲染气泡的宽度同步为聊天区当前宽度的 75%，并重算高度。
// 气泡文本用 QTextEdit（只读），高度 = document 在 viewport 宽度下的实际渲染高度，
// 与 QLabel 的 sizeHint/heightForWidth 不可靠链完全解耦。
func (g *gui) updateBubbleWidths() {
	max := g.bubbleMaxWidth()
	for _, b := range g.bubbles {
		b.box.SetMaximumWidth(max)
		g.fixBubbleHeight(b)
	}
	// 聊天区宽度变化时同步命令块标题的截断宽度。
	for _, blk := range g.cmdBlocks {
		g.setCmdTitle(blk)
	}
}

// fixEditHeight 将气泡 QTextEdit 的高度固定为文档实际渲染高度。
// 必须调用 document.SetTextWidth(viewport 宽) 让换行与显示宽度一致，再取 Size().Height()。
// 创建时 viewport 可能尚未布局（宽为 0），用当前气泡最大宽度估算。
func (g *gui) fixEditHeight(e *qt.QTextEdit) {
	w := e.Viewport().Width()
	if w <= 0 {
		w = g.bubbleMaxWidth()
	}
	doc := e.Document()
	doc.SetTextWidth(float64(w))
	e.SetFixedHeight(int(doc.Size().Height()))
}

// fixBubbleHeight 按文本实际渲染高度固定气泡高度（box 与 edit 高度一致）。
// 必须调用 document.SetTextWidth(viewport 宽) 让换行与显示宽度一致，再取 Size().Height()。
// 创建时 viewport 可能尚未布局（宽为 0），用当前气泡最大宽度估算。
func (g *gui) fixBubbleHeight(b *bubble) {
	w := b.ed.Viewport().Width()
	if w <= 0 {
		w = g.bubbleMaxWidth()
	}
	doc := b.ed.Document()
	doc.SetTextWidth(float64(w))
	h := int(doc.Size().Height())
	b.ed.SetFixedHeight(h)
	b.box.SetFixedHeight(h)
}

// newBubbleEdit 创建圆角消息气泡：圆角背景 QFrame + 透明背景 QTextEdit。
// 文本高度由 fixBubbleHeight 精确控制（与渲染引擎同一 document，杜绝 QLabel 高度虚高）。
func (g *gui) newBubbleEdit(background, text string, markdown bool) *bubble {
	box := qt.NewQFrame2()
	box.SetStyleSheet("QFrame { background:" + background + "; border:none; border-radius:12px; }")
	box.SetSizePolicy2(qt.QSizePolicy__Preferred, qt.QSizePolicy__Fixed)
	box.SetMaximumWidth(g.bubbleMaxWidth())

	ed := qt.NewQTextEdit2()
	ed.SetReadOnly(true)
	// 任意位置换行：WordWrap 只在单词边界断行，超长无空格内容（长 URL/代码行）会撑宽控件。
	ed.SetWordWrapMode(qt.QTextOption__WrapAtWordBoundaryOrAnywhere)
	ed.SetFrameShape(qt.QFrame__NoFrame)
	ed.SetVerticalScrollBarPolicy(qt.ScrollBarAlwaysOff)
	ed.SetHorizontalScrollBarPolicy(qt.ScrollBarAlwaysOff)
	ed.SetStyleSheet("QTextEdit { background:transparent; color:#ffffff; font-size:14px; border:none; }")
	ed.Document().SetDocumentMargin(8)
	ed.SetSizePolicy2(qt.QSizePolicy__Expanding, qt.QSizePolicy__Fixed)
	if markdown {
		ed.SetMarkdown(text)
	} else {
		ed.SetPlainText(text)
	}

	bl := qt.NewQHBoxLayout2()
	bl.SetContentsMargins(0, 0, 0, 0)
	bl.SetSpacing(0)
	bl.AddWidget(ed.QWidget)
	box.SetLayout(bl.QLayout)

	b := &bubble{box: box, ed: ed}
	g.fixBubbleHeight(b)
	return b
}

// newCmdOutputEdit 创建命令输出文本区（只读 QTextEdit，等宽字体，深色底）。
func (g *gui) newCmdOutputEdit(content string) *qt.QTextEdit {
	out := qt.NewQTextEdit2()
	out.SetReadOnly(true)
	// 任意位置换行：避免超长命令输出（无空格长行）撑宽控件导致横向滚动条。
	out.SetWordWrapMode(qt.QTextOption__WrapAtWordBoundaryOrAnywhere)
	out.SetFrameShape(qt.QFrame__NoFrame)
	out.SetVerticalScrollBarPolicy(qt.ScrollBarAlwaysOff)
	out.SetHorizontalScrollBarPolicy(qt.ScrollBarAlwaysOff)
	out.SetStyleSheet("QTextEdit { background:#1f2428; color:#d4d4d4; border:1px solid #3a3f4b; border-radius:6px; font-family:Consolas; font-size:13px; }")
	out.Document().SetDocumentMargin(4)
	// 水平策略用 Ignored：让输出区填满折叠块宽度即可，其 sizeHint（未折行文档的理想宽度）
	// 不能参与 chatBox 的 sizeHint 计算，否则"输出变宽 → chatBox sizeHint 变大 → chatBox
	// 被 QScrollArea 撑宽 → 输出再变宽"的循环会让聊天区宽度无限增长。
	out.SetSizePolicy2(qt.QSizePolicy__Ignored, qt.QSizePolicy__Fixed)
	out.SetPlainText(content)
	g.fixEditHeight(out)
	out.SetVisible(false)
	return out
}

// setStatus 更新顶部状态标签（可从任意 goroutine 调用）。
func (g *gui) setStatus(text string) {
	g.post(func() { g.statusLabel.SetText(text) })
}

// rebuildIndex 手动重建代码图索引（后台执行，不阻塞 UI；Store 内部有锁，
// 与任务结束后的自动重建并发安全）。重建期间按钮禁用并防重入。
func (g *gui) rebuildIndex() {
	if g.cgRebuild {
		return
	}
	g.cgRebuild = true
	g.setStatus("正在重建代码图索引…")
	g.cgBtn.SetEnabled(false)
	go func() {
		start := time.Now()
		ix, err := g.app.Codegraph.Reindex(g.app.CodegraphRoot)
		elapsed := time.Since(start).Round(time.Millisecond)
		g.post(func() {
			g.cgRebuild = false
			g.cgBtn.SetEnabled(true)
			if err != nil {
				g.setStatus("[错误] 索引重建失败: " + err.Error())
				return
			}
			g.setStatus(fmt.Sprintf("索引重建完成: %d 符号 / %d 关系（%v）", len(ix.Nodes), len(ix.Edges), elapsed))
		})
	}()
}

// refreshSidebar 刷新右侧修改文件列表（QTimer 主线程调用）。
func (g *gui) refreshSidebar() {
	files := tools.ModifiedFiles()
	g.sideList.Clear()
	if len(files) == 0 {
		g.sideList.AddItem("无修改文件")
		return
	}
	g.sideList.AddItems(files)
}

// toggleSessionPanel 收起/展开左侧会话列表。
// 收起：面板收窄为竖直窄条（箭头居中、整条可点击），空出的宽度转给聊天区；
// 展开：恢复收起前记住的宽度，宽度可在 min~max 范围拖拽调整（QSplitter）。
func (g *gui) toggleSessionPanel() {
	if g.sessionList.IsVisible() {
		g.sessionPanelWidth = g.splitter.Sizes()[0] // 记住当前展开宽度，展开时恢复
		g.sessionPanel.SetMinimumWidth(panelClosedWidth)
		g.sessionPanel.SetMaximumWidth(panelClosedWidth)
		g.sessionList.SetVisible(false)
		g.sessionToggle.SetText("▶")
		g.sessionToggle.SetSizePolicy2(qt.QSizePolicy__Preferred, qt.QSizePolicy__Expanding)
		g.sessionToggle.SetStyleSheet(panelClosedStyle)
	} else {
		g.sessionPanel.SetMinimumWidth(sessionPanelMinWidth)
		g.sessionPanel.SetMaximumWidth(sessionPanelMaxWidth)
		g.sessionList.SetVisible(true)
		g.sessionToggle.SetText("▾ 会话")
		g.sessionToggle.SetSizePolicy2(qt.QSizePolicy__Preferred, qt.QSizePolicy__Preferred)
		g.sessionToggle.SetStyleSheet(panelHeaderStyle)
	}
	g.relayoutPanels()
}

// toggleFilePanel 收起/展开右侧修改文件列表。
func (g *gui) toggleFilePanel() {
	if g.sideList.IsVisible() {
		g.filePanelWidth = g.splitter.Sizes()[2]
		g.filePanel.SetMinimumWidth(panelClosedWidth)
		g.filePanel.SetMaximumWidth(panelClosedWidth)
		g.sideList.SetVisible(false)
		g.fileToggle.SetText("◀")
		g.fileToggle.SetSizePolicy2(qt.QSizePolicy__Preferred, qt.QSizePolicy__Expanding)
		g.fileToggle.SetStyleSheet(panelClosedStyle)
	} else {
		g.filePanel.SetMinimumWidth(filePanelMinWidth)
		g.filePanel.SetMaximumWidth(filePanelMaxWidth)
		g.sideList.SetVisible(true)
		g.fileToggle.SetText("▾ 修改文件")
		g.fileToggle.SetSizePolicy2(qt.QSizePolicy__Preferred, qt.QSizePolicy__Preferred)
		g.fileToggle.SetStyleSheet(panelHeaderStyle)
	}
	g.relayoutPanels()
}

// relayoutPanels 重新分配三栏宽度：收起的面板固定为窄条并贴边，聊天区占满其余空间。
// QSplitter 在子部件 min/max 尺寸变化后不会自动重排内部尺寸，必须显式 SetSizes，
// 否则会出现窄条不贴边、聊天区不扩展的空隙问题。
func (g *gui) relayoutPanels() {
	sizes := g.splitter.Sizes()
	if len(sizes) != 3 {
		return
	}
	total := sizes[0] + sizes[1] + sizes[2]

	left := g.sessionPanelWidth
	if !g.sessionList.IsVisible() {
		left = panelClosedWidth
	} else if left < sessionPanelMinWidth {
		left = sessionPanelMinWidth
	}

	right := g.filePanelWidth
	if !g.sideList.IsVisible() {
		right = panelClosedWidth
	} else if right < filePanelMinWidth {
		right = filePanelMinWidth
	}

	chat := total - left - right
	if chat < 0 {
		chat = 0
	}
	g.splitter.SetSizes([]int{left, chat, right})
	g.updateBubbleWidths() // 聊天区宽度已变化，同步气泡最大宽度
}

// refreshSessions 刷新左侧会话列表（latest 置顶，其余按时间倒序）。
func (g *gui) refreshSessions() {
	names, err := session.List(g.app.SessionDir)
	if err != nil {
		return
	}
	g.sessionList.Clear()
	if len(names) == 0 {
		g.sessionList.AddItem("（暂无会话）")
		return
	}
	display := make([]string, 0, len(names))
	display = append(display, "latest") // latest 置顶
	for _, n := range names {
		if n == "latest.json" {
			continue
		}
		display = append(display, strings.TrimSuffix(n, ".json"))
	}
	g.sessionList.AddItems(display)
}

// restoreLatest 启动时自动恢复 latest 会话（若存在），方便从上一次进度继续。
func (g *gui) restoreLatest() {
	msgs, err := session.Load(g.app.SessionDir, "latest")
	if err != nil {
		return
	}
	if len(msgs) == 0 {
		return
	}
	g.app.Agent.SetMessages(msgs)
	g.renderMessages(msgs)
	g.setStatus("已恢复上次会话（latest）")
}

// loadSession 点击会话列表项：加载并渲染该会话，替换当前对话上下文。
func (g *gui) loadSession(name string) {
	if name == "" || name == "（暂无会话）" {
		return
	}
	g.mu.Lock()
	running := g.running
	g.mu.Unlock()
	if running {
		g.setStatus("任务运行中，请先停止再切换会话")
		return
	}
	msgs, err := session.Load(g.app.SessionDir, name)
	if err != nil {
		g.setStatus("[错误] 加载会话失败: " + err.Error())
		return
	}
	g.app.Agent.SetMessages(msgs)
	g.clearChat()
	g.renderMessages(msgs)
	g.setStatus("已切换会话: " + name)
}

// clearChat 清空聊天区所有消息与命令块（须在主线程调用）。
func (g *gui) clearChat() {
	g.aiEdit = nil
	g.aiRow = nil
	g.aiBubble = nil
	g.bubbles = nil
	g.cmdBlocks = nil
	g.mu.Lock()
	g.aiMarkdown = ""
	g.aiFlushPending = false
	g.curBlock = nil
	g.mu.Unlock()
	for g.chatLayout.Count() > 0 {
		item := g.chatLayout.TakeAt(0)
		if w := item.Widget(); w != nil {
			w.DeleteLater()
		}
	}
}

// renderMessages 将会话历史渲染为气泡与命令折叠块（须在主线程调用）。
// tool 消息对应一条命令输出折叠块；标题取实际命令（exec_command 的 command 参数），
// 取不到时回退到工具名。
func (g *gui) renderMessages(msgs []llm.Message) {
	toolNames := make(map[string]string)
	toolDetails := make(map[string]string)
	for _, m := range msgs {
		switch m.Role {
		case "user":
			g.addUserMessageUI(m.Content)
		case "assistant":
			for _, tc := range m.ToolCalls {
				toolNames[tc.ID] = tc.Function.Name
				var args map[string]any
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err == nil {
					if cmd, ok := args["command"].(string); ok && strings.TrimSpace(cmd) != "" {
						toolDetails[tc.ID] = strings.TrimSpace(cmd)
					}
				}
			}
			if m.Content != "" {
				g.addAIMessageStaticUI(m.Content)
			}
		case "tool":
			detail := toolDetails[m.ToolCallID]
			if detail == "" {
				detail = toolNames[m.ToolCallID]
			}
			if detail == "" {
				detail = "工具输出"
			}
			// 命令输出在采集源头已统一为 UTF-8（PowerShell 注入 OutputEncoding + decodeCmdLine
			// 自动转码/还原），会话内保存的就是干净文本，加载旧 session 时无需再修复编码。
			g.addCmdBlockStaticUI(detail, m.Content)
		}
	}
	g.scrollToBottom()
}

// wireHeaderContextMenu 为命令块标题挂右键菜单：选择"复制命令"可将实际命令写入剪贴板。
// 用 CustomContextMenu + 手动弹出 QMenu，而不是给按钮 AddAction —— QToolButton 一旦
// 带 action 就会被标记 HasMenu，在最右侧绘制一个向下展开箭头。
func (g *gui) wireHeaderContextMenu(header *qt.QToolButton, blk *cmdBlock) {
	header.SetContextMenuPolicy(qt.CustomContextMenu)
	var menu *qt.QMenu // 闭包内懒创建并复用，避免每次右键新建累积
	header.OnCustomContextMenuRequested(func(pos *qt.QPoint) {
		if menu == nil {
			menu = qt.NewQMenu(header.QWidget)
			act := qt.NewQAction2("复制命令")
			act.OnTriggered(func() { qt.QGuiApplication_Clipboard().SetText(blk.detail) })
			// QMenu 显示的动作就是 widget action 列表（C++ 的 QMenu::addAction 内部
			// 同样走 QWidget::addAction），因此这里直接 AddAction 即可正常显示。
			menu.AddAction(act)
		}
		menu.ExecWithPos(header.MapToGlobalWithQPoint(pos))
	})
}

// addCmdBlockStaticUI 渲染一条历史命令输出折叠块（默认折叠，标题可点击展开）。
// detail 为实际命令（exec_command 的 command），非命令工具时为工具名。
func (g *gui) addCmdBlockStaticUI(detail, content string) {
	blk := &cmdBlock{detail: oneLine(detail), content: content}
	box := qt.NewQFrame2()
	box.SetStyleSheet("QFrame { border:1px solid #444c56; border-radius:10px; background:#2d333b; }")
	box.SetSizePolicy2(qt.QSizePolicy__Preferred, qt.QSizePolicy__Fixed)
	boxLayout := qt.NewQVBoxLayout2()
	boxLayout.SetSpacing(2)
	boxLayout.SetContentsMargins(8, 2, 8, 2) // 折叠时更紧凑：标题行贴近上下边框
	box.SetLayout(boxLayout.QLayout)
	blk.box = box

	header := qt.NewQToolButton2()
	header.SetToolButtonStyle(qt.ToolButtonTextOnly)
	header.SetStyleSheet("QToolButton { border:none; color:#79c0ff; font-weight:bold; padding:2px 8px; text-align:left; } QToolButton:hover { color:#58a6ff; }")
	// 水平策略用 Ignored：标题文本按当前宽度截断（setCmdTitle 的 ElidedText），
	// 若其 sizeHint/最小宽度参与折叠块布局，会形成"标题宽度跟随 box 宽度 → box
	// 最小宽度跟随标题 → chatBox 被 QScrollArea 撑宽"的无限增长环。
	header.SetSizePolicy2(qt.QSizePolicy__Ignored, qt.QSizePolicy__Preferred)
	header.OnClicked(func() { g.toggleBlock(blk) })
	boxLayout.AddWidget(header.QWidget)
	blk.header = header
	g.wireHeaderContextMenu(header, blk)

	output := g.newCmdOutputEdit(content)
	boxLayout.AddWidget(output.QWidget)
	blk.output = output

	g.chatLayout.AddWidget(box.QWidget)
	g.cmdBlocks = append(g.cmdBlocks, blk)
	g.setCmdTitle(blk) // 初始标题按当前宽度截断
}

// oneLine 将详情规范为单行：换行替换为空格，去首尾空白。
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	return strings.TrimSpace(s)
}

// setCmdTitle 设置命令块标题：箭头 + 实际命令，超出可用宽度时行尾截断加省略号。
// 在创建、折叠/展开（箭头变化）、聊天区宽度变化时调用。
func (g *gui) setCmdTitle(blk *cmdBlock) {
	title := blk.detail
	if blk.expanded {
		title = "▾ " + title
	} else {
		title = "▸ " + title
	}
	// 可用宽度 = box 内宽 - 左右 margins（8*2）；创建时 box 尚未布局，用聊天区宽度估算。
	w := blk.box.Width() - 16
	if blk.box.Width() <= 0 {
		w = g.chatBox.Width() - 24 - 16 // chatLayout margins 12*2 + box margins 8*2
	}
	if w < 60 {
		w = 60
	}
	if f := blk.header.Font(); f != nil {
		fm := qt.NewQFontMetrics(f)
		title = fm.ElidedText(title, qt.ElideRight, w)
	}
	blk.header.SetText(title)
}

// addAIMessageStaticUI 添加一条静态 AI 消息气泡（历史会话渲染用）。
func (g *gui) addAIMessageStaticUI(text string) {
	row := qt.NewQWidget2()
	rowLayout := qt.NewQHBoxLayout2()
	rowLayout.SetContentsMargins(0, 0, 0, 0) // 默认 9px margins 会让行高比气泡高，产生上下空隙
	row.SetLayout(rowLayout.QLayout)
	row.SetSizePolicy2(qt.QSizePolicy__Preferred, qt.QSizePolicy__Fixed)

	b := g.newBubbleEdit("#3a3f4b", text, true)
	// widget stretch=1 优先于 spacer(stretch=0) 吸收额外空间，气泡才扩展到最大宽度。
	rowLayout.AddWidget2(b.box.QWidget, 1)
	rowLayout.AddStretchWithStretch(0)
	g.bubbles = append(g.bubbles, b)

	g.chatLayout.AddWidget(row)
}

// ---- 消息气泡与命令折叠块（须在主线程调用） ----

// addUserMessageUI 追加一条用户消息气泡（右对齐、蓝底）。
func (g *gui) addUserMessageUI(text string) {
	row := qt.NewQWidget2()
	rowLayout := qt.NewQHBoxLayout2()
	rowLayout.SetContentsMargins(0, 0, 0, 0)
	row.SetLayout(rowLayout.QLayout)
	row.SetSizePolicy2(qt.QSizePolicy__Preferred, qt.QSizePolicy__Fixed) // 防垂直拉伸

	b := g.newBubbleEdit("#1976d2", text, false)
	rowLayout.AddStretchWithStretch(0)
	rowLayout.AddWidget2(b.box.QWidget, 1)
	g.bubbles = append(g.bubbles, b)

	g.chatLayout.AddWidget(row)
}

// newAIMessageUI 创建当前 AI 回复的消息气泡（左对齐、深灰底、白色 Markdown）。
func (g *gui) newAIMessageUI() {
	row := qt.NewQWidget2()
	rowLayout := qt.NewQHBoxLayout2()
	rowLayout.SetContentsMargins(0, 0, 0, 0)
	row.SetLayout(rowLayout.QLayout)
	row.SetSizePolicy2(qt.QSizePolicy__Preferred, qt.QSizePolicy__Fixed) // 防垂直拉伸

	b := g.newBubbleEdit("#3a3f4b", "", true)
	rowLayout.AddWidget2(b.box.QWidget, 1)
	rowLayout.AddStretchWithStretch(0)
	g.bubbles = append(g.bubbles, b)

	g.chatLayout.AddWidget(row)
	g.aiRow = row
	g.aiEdit = b.ed
	g.aiBubble = b
}

// newCmdBlockUI 创建命令折叠块（默认折叠，标题可点击展开）。
func (g *gui) newCmdBlockUI(blk *cmdBlock, detail string) {
	blk.detail = oneLine(detail)
	box := qt.NewQFrame2()
	box.SetStyleSheet("QFrame { border:1px solid #444c56; border-radius:10px; background:#2d333b; }")
	box.SetSizePolicy2(qt.QSizePolicy__Preferred, qt.QSizePolicy__Fixed) // 防垂直拉伸
	boxLayout := qt.NewQVBoxLayout2()
	boxLayout.SetSpacing(2)
	boxLayout.SetContentsMargins(8, 2, 8, 2) // 折叠时更紧凑：标题行贴近上下边框
	box.SetLayout(boxLayout.QLayout)
	blk.box = box

	header := qt.NewQToolButton2()
	header.SetToolButtonStyle(qt.ToolButtonTextOnly)
	header.SetStyleSheet("QToolButton { border:none; color:#79c0ff; font-weight:bold; padding:2px 8px; text-align:left; } QToolButton:hover { color:#58a6ff; }")
	// 水平策略用 Ignored：标题文本按当前宽度截断（setCmdTitle 的 ElidedText），
	// 若其 sizeHint/最小宽度参与折叠块布局，会形成"标题宽度跟随 box 宽度 → box
	// 最小宽度跟随标题 → chatBox 被 QScrollArea 撑宽"的无限增长环。
	header.SetSizePolicy2(qt.QSizePolicy__Ignored, qt.QSizePolicy__Preferred)
	header.OnClicked(func() { g.toggleBlock(blk) })
	boxLayout.AddWidget(header.QWidget)
	blk.header = header
	g.wireHeaderContextMenu(header, blk)

	output := g.newCmdOutputEdit("")
	boxLayout.AddWidget(output.QWidget)
	blk.output = output

	g.chatLayout.AddWidget(box.QWidget)
	g.cmdBlocks = append(g.cmdBlocks, blk)
	g.setCmdTitle(blk)
}

// toggleBlock 点击标题切换折叠/展开（主线程调用）。
// 注意：切换时保持当前滚动位置，不滚动到底部（避免打断用户阅读）。
func (g *gui) toggleBlock(blk *cmdBlock) {
	blk.expanded = !blk.expanded
	blk.output.SetVisible(blk.expanded)
	if blk.expanded {
		g.fixEditHeight(blk.output) // 展开时按当前宽度重算高度
	}
	g.setCmdTitle(blk) // 箭头随折叠状态变化并重新截断
}

// ---- Agent 回调（从 agent goroutine 调用，线程安全） ----

// pushText AI 流式文本增量：累积 Markdown，节流合并渲染。
func (g *gui) pushText(s string) {
	g.mu.Lock()
	g.aiMarkdown += s
	if !g.aiFlushPending {
		g.aiFlushPending = true
		time.AfterFunc(50*time.Millisecond, func() {
			g.mu.Lock()
			g.aiFlushPending = false
			g.mu.Unlock()
			g.post(g.renderAI)
		})
	}
	g.mu.Unlock()
}

// renderAI 渲染当前 AI 回复（须在主线程执行）。
func (g *gui) renderAI() {
	g.mu.Lock()
	md := g.aiMarkdown
	g.mu.Unlock()
	if md == "" {
		return
	}
	if g.aiEdit == nil {
		g.newAIMessageUI()
	}
	g.aiEdit.SetMarkdown(md)
	if g.aiBubble != nil {
		g.fixBubbleHeight(g.aiBubble)
	} else {
		g.fixEditHeight(g.aiEdit)
	}
	g.scrollToBottom()
}

// pushToken 更新上下文用量标签。
func (g *gui) pushToken(used, limit int) {
	g.post(func() { g.tokenLabel.SetText(fmt.Sprintf("上下文: %d/%d", used, limit)) })
}

// pushUsage 更新为最近一轮 LLM 调用的服务端真实缓存命中统计（用于费用监控）。
func (g *gui) pushUsage(hit, miss int) {
	g.post(func() { g.tokenLabel.SetText(fmt.Sprintf("缓存命中: %d | 未命中: %d", hit, miss)) })
}

// askContinue 在后台 goroutine 中调用：工具调用次数达到上限时弹窗询问用户是否继续。
// 弹窗必须在 Qt 主线程弹出，故把任务阻塞式投递到主线程并同步等待用户选择；
// updates 队列满时重试投递，避免弹窗被丢弃导致后台 goroutine 永久阻塞。
func (g *gui) askContinue(used int) bool {
	done := make(chan bool, 1)
	for {
		select {
		case g.updates <- func() {
			res := qt.QMessageBox_Question(g.win.QWidget, "继续执行？",
				fmt.Sprintf("本轮已执行 %d 次工具调用，已达到上限。是否继续执行？", used))
			done <- res == qt.QMessageBox__Yes
		}:
			return <-done
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// askUser 在后台 goroutine 中调用：弹出输入对话框向用户提问（Qt 主线程）。
// 与 askContinue 相同的阻塞式投递策略，保证对话框一定在主线程弹出。
func (g *gui) askUser(question string) string {
	done := make(chan string, 1)
	for {
		select {
		case g.updates <- func() {
			done <- qt.QInputDialog_GetText(g.win.QWidget, "请回答", question)
		}:
			return <-done
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// blockStart 工具开始执行：结束当前 AI 回复，准备命令折叠块（默认展开）。
// detail 为工具的可读摘要（exec_command 为实际命令，其余为工具名）。
func (g *gui) blockStart(name, detail string) {
	blk := &cmdBlock{}
	g.mu.Lock()
	g.curBlock = blk
	g.mu.Unlock()
	// 所有 aiMarkdown/aiEdit 的清理都在主线程执行，避免数据竞态。
	g.post(func() {
		g.mu.Lock()
		g.aiMarkdown = ""
		g.aiFlushPending = false
		g.aiEdit = nil
		g.aiRow = nil
		g.aiBubble = nil
		g.mu.Unlock()
		g.newCmdBlockUI(blk, detail)
		g.scrollToBottom()
	})
}

// cmdLine 命令输出行：追加到当前折叠块（执行中保持展开）。
func (g *gui) cmdLine(line string) {
	g.mu.Lock()
	blk := g.curBlock
	g.mu.Unlock()
	if blk == nil {
		return
	}
	g.post(func() {
		if blk.output == nil {
			return
		}
		blk.content += line
		blk.output.SetPlainText(blk.content)
		g.fixEditHeight(blk.output)
		if !blk.expanded {
			blk.expanded = true
			g.setCmdTitle(blk)
		}
		blk.output.SetVisible(true)
		g.scrollToBottom()
	})
}

// flushCmd 命令执行完毕：自动折叠当前折叠块。
func (g *gui) flushCmd() {
	g.mu.Lock()
	blk := g.curBlock
	g.mu.Unlock()
	if blk == nil {
		return
	}
	g.post(func() {
		if blk.output == nil {
			return
		}
		blk.expanded = false
		blk.output.SetVisible(false)
		g.setCmdTitle(blk)
		g.scrollToBottom()
	})
}

// ---- 交互（主线程事件处理器调用） ----

// onSend 发送用户输入并启动 Agent 运行。
func (g *gui) onSend() {
	text := strings.TrimSpace(g.input.Text())
	if text == "" {
		return
	}
	g.mu.Lock()
	if g.running {
		g.mu.Unlock()
		return
	}
	g.running = true
	g.mu.Unlock()

	g.sendBtn.SetEnabled(false)
	g.input.Clear()
	g.pushUser(text)
	g.setStatus("运行中…")

	ctx, cancel := context.WithCancel(context.Background())
	g.mu.Lock()
	g.cancel = cancel
	g.mu.Unlock()

	ag := g.app.Agent
	app := g.app
	go func() {
		_, err := ag.Run(ctx, text)
		g.mu.Lock()
		stillRunning := g.running
		g.mu.Unlock()
		if stillRunning {
			g.finishTurn(err)
		}
		if err == nil {
			if _, serr := session.Save(app.SessionDir, "latest", ag.Messages()); serr != nil {
				logx.Warn("自动保存会话失败: %v", serr)
			}
			// 同时保存时间戳快照，供左侧会话列表切换历史进度。
			if _, serr := session.Save(app.SessionDir, "", ag.Messages()); serr != nil {
				logx.Warn("保存会话快照失败: %v", serr)
			}
			g.post(g.refreshSessions)
		} else {
			// 输出中断（手动停止或断网）：部分内容已保留在 Agent 消息中，保存 latest 以便重启后续写。
			if _, serr := session.Save(app.SessionDir, "latest", ag.Messages()); serr != nil {
				logx.Warn("自动保存会话失败: %v", serr)
			}
		}
	}()
}

// pushUser 在聊天区追加一条用户消息。
func (g *gui) pushUser(text string) {
	g.post(func() {
		g.addUserMessageUI(text)
		g.scrollToBottom()
	})
}

// finishTurn 结束一轮运行，恢复按钮状态。
func (g *gui) finishTurn(err error) {
	g.mu.Lock()
	g.running = false
	g.cancel = nil
	g.mu.Unlock()

	switch {
	case err == nil:
		g.setStatus("就绪")
	case strings.Contains(err.Error(), "context canceled"):
		g.pushSys("[已停止]")
		g.setStatus("已停止")
		logx.Warn("本轮任务被用户中断")
	default:
		g.pushSys("[错误] " + err.Error())
		g.setStatus("[错误] " + err.Error())
		logx.Error("任务执行错误: %v", err)
	}
	g.post(func() { g.sendBtn.SetEnabled(true) })
}

// pushSys 在聊天区追加一条居中灰色系统消息。
func (g *gui) pushSys(text string) {
	g.post(func() {
		row := qt.NewQWidget2()
		rowLayout := qt.NewQHBoxLayout2()
		row.SetLayout(rowLayout.QLayout)
		rowLayout.AddStretch()
		lbl := qt.NewQLabel3(text)
		lbl.SetStyleSheet("color:#9da5b4; font-size:13px;")
		rowLayout.AddWidget(lbl.QWidget)
		rowLayout.AddStretch()
		g.chatLayout.AddWidget(row)
		g.scrollToBottom()
	})
}

// onStop 取消当前任务（主线程调用）。
func (g *gui) onStop() {
	g.mu.Lock()
	cancel := g.cancel
	g.mu.Unlock()
	if cancel != nil {
		cancel()
		g.setStatus("正在停止…")
	}
}
