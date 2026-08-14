// Package bootstrap 提供 CLI 与 GUI 共用的启动装配逻辑。
package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"go-agent/internal/agent"
	"go-agent/internal/codegraph"
	"go-agent/internal/config"
	"go-agent/internal/embed"
	"go-agent/internal/llm"
	"go-agent/internal/logx"
	"go-agent/internal/search"
	"go-agent/internal/semantic"
	"go-agent/internal/skill"
	"go-agent/internal/tools"
)

// Options 是装配时注入的回调（CLI/GUI 各自实现）。
type Options struct {
	OnContent     func(content string)
	OnReasoning   func(content string) // 模型思考模式推理文本增量（GUI 流式展示用；nil = 不展示）
	OnToolStart   func(name, detail string)
	OnToolOutput  func(line string)
	OnToolEnd     func(name string)
	OnTokenCount  func(used, limit int)
	OnUsage       func(hit, miss int)
	OnAskContinue func(used int) bool
	// AskUser 模型通过 ask_user 工具向用户提问时的通道；返回空串表示用户未回答/取消。
	AskUser func(question string) string
}

// App 是装配完成的运行时对象。
type App struct {
	Cfg        *config.Config
	Client     *llm.Client
	Registry   *tools.Registry
	Agent      *agent.Agent
	SearchMgr  *search.Manager
	Skills     []*skill.Skill
	SessionDir string
	// Codegraph 代码图索引（符号/调用关系），任务完成后自动增量重建。
	Codegraph *codegraph.Store
	// CodegraphRoot 索引的项目根目录（随工作目录切换，与文件工具一致）。
	CodegraphRoot string
	// Workspace 当前工作目录（Agent 相对路径的基准；SetWorkspace 会同步 os.Chdir）。
	Workspace string
	// CGSet codegraph 工具集（工作目录切换时同步 Root）。
	CGSet *tools.CodegraphToolSet
	// Semantic 语义检索服务（EMBED_MODEL 配置了 GGUF 模型时非 nil）。
	Semantic *semantic.Service
	// SemanticDone 在语义检索初始化完成后关闭（成功或失败都会关闭）；
	// 后台重建索引前等待它，确保首轮索引包含语义向量。未配置模型时立即关闭。
	SemanticDone chan struct{}
	// CGEvent 代码图重建生命周期回调（nil = 不报告）。在重建 goroutine 同步调用，
	// 调用方应尽快返回（UI 更新请自行投递到主线程）。
	CGEvent func(e CGEvent)

	wsMu         sync.Mutex // 串行化工作目录切换与 codegraph 索引重建
	codegraphWSD string     // 当前 Codegraph 索引对应的工作目录（避免重复加载）
}

// CGEvent 代码图重建生命周期事件。
type CGEvent struct {
	Kind        string // "start" | "progress" | "done"
	Phase       codegraph.ProgressPhase
	Done, Total int
}

// Setup 完成全部启动装配。调用方负责在退出时调用 app.Close()。
func Setup(opts Options) (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("加载配置失败: %w", err)
	}
	if cfg.LLM.APIKey == "" {
		p, _ := config.Path()
		return nil, fmt.Errorf("未配置 LLM API Key。设置环境变量 DEEPSEEK_API_KEY 或编辑配置文件 %s", p)
	}

	app := &App{Cfg: cfg}

	// 搜索
	app.SearchMgr = search.NewManager(cfg.Search.Backend, cfg.Search.BraveAPIKey, cfg.Search.SearxngURL)

	// 工具
	app.Registry = tools.NewRegistry()
	app.Registry.Register(&tools.ReadFileRangeTool{})
	app.Registry.Register(&tools.WriteFileTool{})
	app.Registry.Register(&tools.EditFileTool{})
	app.Registry.Register(&tools.SearchFileNamesTool{})
	app.Registry.Register(&tools.SearchFileContentTool{})
	app.Registry.Register(&tools.ExecCommandTool{OnOutput: opts.OnToolOutput})
	app.Registry.Register(&tools.CheckCommandStatusTool{})
	app.Registry.Register(&tools.WebSearchTool{Manager: app.SearchMgr})
	app.Registry.Register(&tools.ReadSkillTool{Dir: skill.DefaultDir()})
	app.Registry.Register(tools.NewListSkillsTool(func() []*skill.Skill { return app.Skills }))
	app.Registry.Register(&tools.WebFetchTool{})
	app.Registry.Register(&tools.ListDirTool{})
	app.Registry.Register(&tools.GetCurrentTimeTool{})
	app.Registry.Register(&tools.GetEnvTool{})
	app.Registry.Register(tools.NewAskUserTool(func(question string) string {
		if opts.AskUser != nil {
			return opts.AskUser(question)
		}
		return ""
	}))

	// 技能
	app.Skills, err = skill.Load("")
	if err != nil {
		logx.Warn("加载技能失败: %v", err)
	}

	// 代码图索引：启动时加载已有索引（若存在），任务完成后后台增量重建
	wd, _ := os.Getwd()
	app.CodegraphRoot = wd
	app.Workspace = wd
	app.codegraphWSD = wd
	app.Codegraph = codegraph.LoadStore(wd)
	app.wireCGProgress() // 索引重建进度桥接到 CGEvent（GUI 展示用；语义就绪后自动补接向量化进度）
	// 语义检索：模型路径来自 config.json embedding.model（EMBED_MODEL 可覆盖，"off"/"0" 关闭）。
	// 加载 llama.cpp 模型较慢，改为后台异步加载（SemanticDone 关闭即就绪/失败），不阻塞窗口出现。
	app.startSemantic(cfg)
	// 细粒度 codegraph 工具集：FTS5 全文检索 + 可选语义重排 + 图遍历
	// （search/node/callers/callees/trace/impact，各自只返回所需子集）
	cgSet := &tools.CodegraphToolSet{
		Index:    func() *codegraph.Index { return app.Codegraph.Index() },
		Root:     wd,
		Semantic: app.Semantic,
		FTS: func(query string, limit int) ([]int, error) {
			db := app.Codegraph.DB()
			if db == nil {
				return nil, nil // 未构建：由工具回退内存词法
			}
			return db.Search(query, limit)
		},
	}
	app.CGSet = cgSet
	app.Registry.Register(&tools.CodegraphSearchTool{CodegraphToolSet: cgSet})
	app.Registry.Register(&tools.CodegraphNodeTool{CodegraphToolSet: cgSet})
	app.Registry.Register(&tools.CodegraphCallersTool{CodegraphToolSet: cgSet})
	app.Registry.Register(&tools.CodegraphCalleesTool{CodegraphToolSet: cgSet})
	app.Registry.Register(&tools.CodegraphTraceTool{CodegraphToolSet: cgSet})
	app.Registry.Register(&tools.CodegraphImpactTool{CodegraphToolSet: cgSet})

	// Agent
	app.Client = llm.NewClient(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Model)

	// 上下文窗口解析：配置显式指定 > 已知模型映射 > 默认 1M。
	// DeepSeek /models 不返回上下文长度，故内置映射表兜底；未知模型走默认并提示。
	cw := cfg.LLM.ContextWindow
	if cw <= 0 {
		cw = llm.ContextWindowFor(cfg.LLM.Model)
	}
	if cw <= 0 {
		cw = 1000000
		logx.Warn("未知模型 %q 的上下文窗口，使用默认 1M（可在 config.json 的 llm.context_window 中覆盖）", cfg.LLM.Model)
	}

	app.Agent = agent.New(app.Client, app.Registry, agent.Options{
		OnContent:     opts.OnContent,
		OnReasoning:   opts.OnReasoning,
		OnToolStart:   opts.OnToolStart,
		OnToolEnd:     opts.OnToolEnd,
		OnTokenCount:  opts.OnTokenCount,
		OnUsage:       opts.OnUsage,
		OnAskContinue: opts.OnAskContinue,
		ContextWindow: cw,
		Skills:        app.Skills,
		// 任务结束（成功/出错/中断）后后台增量重建代码图索引，不阻塞下一次输入
		OnTaskDone: func() {
			go func() {
				<-app.SemanticDone // 等待语义初始化完成，首轮索引包含语义向量
				ix, err := app.ReindexCodegraph()
				if err != nil {
					logx.Warn("codegraph 索引重建失败: %v", err)
					return
				}
				logx.Info("codegraph 索引已更新: %d 符号 / %d 关系", len(ix.Nodes), len(ix.Edges))
			}()
		},
	})

	// 启动后异步预热索引，使首次 codegraph_explore 查询即可用
	go func() {
		<-app.SemanticDone
		if ix, err := app.ReindexCodegraph(); err == nil && ix != nil {
			logx.Info("codegraph 索引就绪: %d 符号 / %d 关系", len(ix.Nodes), len(ix.Edges))
		}
	}()

	// 启动后异步探测模型列表：校验模型名、并在服务端返回上下文长度时动态更新。
	// 不阻塞启动流程，探测失败仅记录警告（上下文沿用上述解析结果）。
	go probeModels(app.Client, app.Agent, cfg.LLM.Model, cfg.LLM.ContextWindow > 0)

	app.SessionDir, _ = config.SessionDir()
	return app, nil
}

// SetWorkspace 切换当前工作目录：同步 os.Chdir，并让 codegraph 索引根/工具集跟随。
// Agent 的相对路径（读文件、执行命令）从此以 dir 为基准。
// 不阻塞调用线程：codegraph 索引在后台切换到新工作区（由 ReindexCodegraph 保证一致性）。
func (a *App) SetWorkspace(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	st, err := os.Stat(abs)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("工作目录不存在: %s", abs)
	}
	if err := os.Chdir(abs); err != nil {
		return err
	}
	a.Workspace = abs
	a.CodegraphRoot = abs
	if a.CGSet != nil {
		a.CGSet.Root = abs
	}
	if a.Codegraph == nil || a.codegraphWSD != abs {
		// 后台切换到新工作区并预热索引（ReindexCodegraph 内部先换 Store 再重建，带锁）。
		go func() {
			<-a.SemanticDone
			if ix, err := a.ReindexCodegraph(); err == nil && ix != nil {
				logx.Info("codegraph 索引就绪: %d 符号 / %d 关系", len(ix.Nodes), len(ix.Edges))
			}
		}()
	}
	return nil
}

// startSemantic 后台加载语义检索（llama.cpp embedding 模型）。模型加载较慢，
// 异步执行避免阻塞窗口出现；SemanticDone 在加载完成（成功或失败）后关闭。
func (a *App) startSemantic(cfg *config.Config) {
	a.SemanticDone = make(chan struct{})
	mp := embedModelPath(cfg)
	if mp == "" {
		close(a.SemanticDone) // 未配置 → 立即就绪（回退 FTS5）
		return
	}
	go func() {
		defer close(a.SemanticDone)
		s, serr := semantic.Load(mp, embed.PoolLast)
		if serr != nil {
			logx.Warn("语义检索启用失败（将回退 FTS5 全文检索）: %v", serr)
			return
		}
		a.wsMu.Lock()
		a.Semantic = s
		a.wireCGProgress() // 语义就绪后补接逐符号向量化进度
		a.wsMu.Unlock()
		logx.Info("语义检索已启用（模型 %s）", mp)
	}()
}

// ReindexCodegraph 重建当前工作区的代码图索引。
// 若工作目录已切换而索引 Store 尚未跟随（SetWorkspace 异步切换），先在此锁内换 Store 再重建；
// 与 SetWorkspace 互斥（避免重建期间旧 Store 被关闭），线程安全。
// 重建结束后上报 CGEvent{Kind:"done"}，供 UI 清理自动重建的状态栏进度文本。
func (a *App) ReindexCodegraph() (*codegraph.Index, error) {
	a.wsMu.Lock()
	defer a.wsMu.Unlock()
	if a.codegraphWSD != a.CodegraphRoot {
		old := a.Codegraph
		a.Codegraph = codegraph.LoadStore(a.CodegraphRoot)
		a.wireCGProgress() // 新 Store 需重新接进度桥
		a.codegraphWSD = a.CodegraphRoot
		if old != nil {
			old.Close()
		}
	}
	ix, err := a.Codegraph.Reindex(a.CodegraphRoot)
	if a.CGEvent != nil {
		a.CGEvent(CGEvent{Kind: "done"})
	}
	return ix, err
}

// wireCGProgress 把 codegraph 重建进度桥接到 CGEvent（CGEvent 为 nil 时不报告）。
// 调用方须持有 wsMu（Reindex 期间）或保证无并发（Setup 阶段）：
//   - OnProgress：扫描/解析/建图/保存阶段的整体进度；
//   - VecBuilder：语义向量化逐符号进度（最耗时阶段，逐符号上报让 UI 看到进展）。
func (a *App) wireCGProgress() {
	if a.Codegraph == nil {
		return
	}
	a.Codegraph.OnProgress = func(p codegraph.Progress) {
		if a.CGEvent != nil {
			a.CGEvent(CGEvent{Kind: "progress", Phase: p.Phase, Done: p.Done, Total: p.Total})
		}
	}
	if a.Semantic != nil {
		a.Codegraph.VecBuilder = a.Semantic.VecBuilderWithProgress(func(done, total int) {
			if a.CGEvent != nil {
				a.CGEvent(CGEvent{Kind: "progress", Phase: codegraph.ProgressVec, Done: done, Total: total})
			}
		})
	} else {
		a.Codegraph.VecBuilder = nil
	}
}

// probeModels 查询 /models 并上报结果。
func probeModels(c *llm.Client, a *agent.Agent, model string, explicitCfg bool) {
	ctx, cancel := context.WithTimeout(context.Background(), llm.ProbeTimeout())
	defer cancel()
	list, err := c.Models(ctx)
	if err != nil {
		logx.Warn("探测可用模型列表失败（%v），上下文窗口沿用配置/默认值", err)
		return
	}
	names := make([]string, 0, len(list))
	found := false
	for _, m := range list {
		names = append(names, m.ID)
		if m.ID == model {
			found = true
			// 服务端明确返回上下文长度且用户未在配置中显式指定时，动态更新
			if m.Context > 0 && !explicitCfg {
				a.SetContextWindow(m.Context)
				logx.Info("已按服务端信息将上下文窗口更新为 %d tokens（模型 %s）", m.Context, model)
			}
		}
	}
	logx.Info("可用模型: %s", strings.Join(names, ", "))
	if !found {
		logx.Warn("当前配置的模型 %q 不在服务端可用列表中，请检查模型名", model)
	}
}

// SystemPromptDumpName 是 --dump-system-prompt 的调试输出文件名。
const SystemPromptDumpName = "system-prompt.dump.txt"

// SystemPromptDumpPath 返回 system prompt 调试文件的完整路径（当前工作目录下）。
func SystemPromptDumpPath() string {
	wd, _ := os.Getwd()
	return filepath.Join(wd, SystemPromptDumpName)
}

// DumpSystemPrompt 将稳定的系统提示词（基础提示词 + 自定义提示词 + 技能摘要）写入调试文件。
// 系统提示词构建后恒定不变，不依赖用户输入。
func (a *App) DumpSystemPrompt(ctx context.Context) error {
	prompt := a.Agent.SystemPrompt()
	return os.WriteFile(SystemPromptDumpPath(), []byte(prompt), 0o644)
}

// DumpSystemPromptFallback 在 Setup 失败（如未配置 API Key）时仍输出 system prompt
// 基线，保证调试功能不依赖 LLM 配置即可生成文件。使用空 client/registry 仅用于
// 构建提示词文本，不会发起任何 LLM 调用。
func DumpSystemPromptFallback() error {
	skills, _ := skill.Load("")
	ag := agent.New(llm.NewClient("", "", "deepseek-chat"), tools.NewRegistry(),
		agent.Options{Skills: skills})
	prompt := ag.SystemPrompt()
	return os.WriteFile(SystemPromptDumpPath(), []byte(prompt), 0o644)
}

// embedModelPath 返回 embedding 模型路径：EMBED_MODEL 环境变量 > config.json embedding.model。
// 值为 "off" 或 "0" 时显式禁用语义检索（返回 ""）。未配置时返回 ""（回退 FTS5，不加载 DLL）。
func embedModelPath(cfg *config.Config) string {
	mp := cfg.Embedding.Model
	if v := os.Getenv("EMBED_MODEL"); v != "" {
		mp = v
	}
	if strings.EqualFold(mp, "off") || mp == "0" {
		return ""
	}
	return mp
}
