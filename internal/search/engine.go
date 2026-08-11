// Package search 提供联网搜索能力，支持 API 后端：
// Brave Search API 与 SearXNG 实例。不再内置 HTML 爬虫后端。
package search

import (
	"context"
	"fmt"
	"strings"

	"go-agent/internal/logx"
)

// Result 是一条搜索结果。
type Result struct {
	Title   string
	URL     string
	Snippet string
}

// Engine 是搜索后端统一接口。
type Engine interface {
	Name() string
	Search(ctx context.Context, query string, maxResults int) ([]Result, error)
}

// Manager 根据配置选择后端，并在主后端失败时降级到另一个已配置的后端。
type Manager struct {
	// Primary 主后端（brave/searxng），由配置决定
	Primary Engine
	// Fallback 备用后端（配置了另一后端时为非 nil）
	Fallback Engine
}

// NewManager 按配置创建搜索管理器。
// backend 取值：brave / searxng。
// brave 需配置 API Key；searxng 需配置实例 URL。
// 当主后端不可用时，若另一后端已配置则作为 fallback。
func NewManager(backend, braveKey, searxngURL string) *Manager {
	m := &Manager{}
	switch strings.ToLower(backend) {
	case "brave":
		if braveKey != "" {
			m.Primary = &Brave{APIKey: braveKey}
		}
	case "searxng":
		if searxngURL != "" {
			m.Primary = &Searxng{BaseURL: searxngURL}
		}
	}
	// 备用后端：优先取配置的另一接口；brave 与 searxng 都配置时，
	// 未选中的那个作为 fallback。
	if strings.ToLower(backend) != "brave" && braveKey != "" {
		m.Fallback = &Brave{APIKey: braveKey}
	}
	if strings.ToLower(backend) != "searxng" && searxngURL != "" {
		m.Fallback = &Searxng{BaseURL: searxngURL}
	}
	return m
}

// Search 使用主后端搜索；失败时自动降级到备用后端，并记录警告。
// 两者都不可用时返回明确错误。
func (m *Manager) Search(ctx context.Context, query string, maxResults int) ([]Result, error) {
	if m.Primary == nil {
		return nil, fmt.Errorf("搜索后端未配置：请在 config.json 中设置 search.backend（brave/searxng）及对应参数（brave_api_key / searxng_url）")
	}
	results, err := m.Primary.Search(ctx, query, maxResults)
	if err != nil {
		if m.Fallback != nil {
			logx.Warn("搜索后端 %s 失败（%v），已降级到备用后端 %s", m.Primary.Name(), err, m.Fallback.Name())
			results, fallbackErr := m.Fallback.Search(ctx, query, maxResults)
			if fallbackErr != nil {
				return nil, fmt.Errorf("搜索失败：主后端 %s（%v），备用后端 %s（%v）",
					m.Primary.Name(), err, m.Fallback.Name(), fallbackErr)
			}
			return results, nil
		}
		return nil, err
	}
	return results, nil
}

// BackendName 返回实际使用的主后端名称（未配置时返回 "未配置"）。
func (m *Manager) BackendName() string {
	if m.Primary == nil {
		return "未配置"
	}
	return m.Primary.Name()
}

// FormatResults 将搜索结果格式化为适合注入 LLM 的文本。
func FormatResults(results []Result) string {
	var b strings.Builder
	for i, r := range results {
		b.WriteString(r.Title + "\n")
		b.WriteString(r.URL + "\n")
		if r.Snippet != "" {
			b.WriteString(r.Snippet + "\n")
		}
		if i < len(results)-1 {
			b.WriteString("---\n")
		}
	}
	return b.String()
}
