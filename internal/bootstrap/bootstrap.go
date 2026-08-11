// Package bootstrap 提供 CLI 与 GUI 共用的启动装配逻辑。
package bootstrap

import (
	"context"
	"fmt"
	"strings"

	"go-agent/internal/agent"
	"go-agent/internal/config"
	"go-agent/internal/llm"
	"go-agent/internal/logx"
	"go-agent/internal/memory"
	"go-agent/internal/search"
	"go-agent/internal/skill"
	"go-agent/internal/tools"
)

// Options 是装配时注入的回调（CLI/GUI 各自实现）。
type Options struct {
	OnContent    func(content string)
	OnToolStart  func(name, detail string)
	OnToolOutput func(line string)
	OnToolEnd    func(name string)
	OnTokenCount func(used, limit int)
}

// App 是装配完成的运行时对象。
type App struct {
	Cfg        *config.Config
	Client     *llm.Client
	Registry   *tools.Registry
	Agent      *agent.Agent
	SearchMgr  *search.Manager
	Skills     []*skill.Skill
	MemLong    *memory.LongTerm
	SessionDir string
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

	// 技能
	app.Skills, err = skill.Load("")
	if err != nil {
		logx.Warn("加载技能失败: %v", err)
	}

	// 记忆
	if mp, perr := config.MemoryPath(); perr == nil {
		if app.MemLong, err = memory.OpenLongTerm(mp); err != nil {
			logx.Warn("打开长期记忆失败: %v", err)
			app.MemLong = nil
		}
	}

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
		OnToolStart:   opts.OnToolStart,
		OnToolEnd:     opts.OnToolEnd,
		OnTokenCount:  opts.OnTokenCount,
		ContextWindow: cw,
		Skills:        app.Skills,
		MemoryLong:    app.MemLong,
	})

	// 启动后异步探测模型列表：校验模型名、并在服务端返回上下文长度时动态更新。
	// 不阻塞启动流程，探测失败仅记录警告（上下文沿用上述解析结果）。
	go probeModels(app.Client, app.Agent, cfg.LLM.Model, cfg.LLM.ContextWindow > 0)

	app.SessionDir, _ = config.SessionDir()
	return app, nil
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

// Close 释放记忆等资源。
func (a *App) Close() {
	if a.MemLong != nil {
		_ = a.MemLong.Close()
	}
}
