package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Config 是全局配置。
type Config struct {
	LLM       LLMConfig       `json:"llm"`
	Search    SearchConfig    `json:"search"`
	Embedding EmbeddingConfig `json:"embedding"`
	// Memory 在后续迭代中实现
	// Memory MemoryConfig `json:"memory"`
}

// EmbeddingConfig 描述本地语义检索（embedding）配置。
type EmbeddingConfig struct {
	// Model GGUF embedding 模型路径（如 D:\models\nomic-embed-text-v1.5.Q8_0.gguf）。
	// 留空或 "off" 时禁用语义检索，回退 FTS5 全文检索（此时不加载 llama_bridge.dll）。
	Model string `json:"model"`
}

// LLMConfig 描述 LLM 连接信息。
type LLMConfig struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
	// ContextWindow 模型上下文窗口 token 数（0 表示使用默认 1M）。
	ContextWindow int `json:"context_window"`
}

// SearchConfig 描述联网搜索配置。
type SearchConfig struct {
	// Backend 搜索后端：brave / searxng
	// （brave 需 API Key；searxng 需实例 URL，两者都未配置时搜索不可用）
	Backend     string `json:"backend"`
	BraveAPIKey string `json:"brave_api_key"`
	SearxngURL  string `json:"searxng_url"`
}

// Default 返回默认配置（DeepSeek 国内服务；搜索需手动配置后端）。
func Default() *Config {
	return &Config{
		LLM: LLMConfig{
			BaseURL:       "https://api.deepseek.com",
			APIKey:        "",
			Model:         "deepseek-chat",
			ContextWindow: 1000000,
		},
		Search: SearchConfig{
			Backend: "",
		},
		Embedding: EmbeddingConfig{},
	}
}

// exeDir 返回程序所在目录（所有数据文件与程序同目录）。
func exeDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

// Path 返回配置文件路径：与程序同目录下的 config.json。
func Path() (string, error) {
	dir, err := exeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// MemoryPath 已移除：长期记忆由 Agent 通过文件工具维护，不再使用 bbolt 数据库。

// SessionDir 返回会话历史目录：<程序目录>/sessions。
func SessionDir() (string, error) {
	dir, err := exeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sessions"), nil
}

// Load 按优先级加载配置：默认值 < 配置文件 < 环境变量。
// 配置文件不存在时自动生成默认配置到程序同目录，方便用户直接编辑。
func Load() (*Config, error) {
	cfg := Default()

	p, err := Path()
	if err != nil {
		return nil, err
	}
	fileExists := false
	if data, err := os.ReadFile(p); err == nil {
		fileExists = true
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	// 首次运行：写入默认配置（尽力而为，目录只读时忽略错误，环境变量仍可兜底）
	if !fileExists {
		_ = cfg.Save()
	}

	// 环境变量覆盖
	if v := os.Getenv("LLM_API_KEY"); v != "" {
		cfg.LLM.APIKey = v
	}
	if v := os.Getenv("LLM_BASE_URL"); v != "" {
		cfg.LLM.BaseURL = v
	}
	if v := os.Getenv("LLM_MODEL"); v != "" {
		cfg.LLM.Model = v
	}
	if v := os.Getenv("DEEPSEEK_API_KEY"); v != "" && cfg.LLM.APIKey == "" {
		cfg.LLM.APIKey = v
	}
	if v := os.Getenv("BRAVE_API_KEY"); v != "" {
		cfg.Search.BraveAPIKey = v
	}
	if v := os.Getenv("SEARXNG_URL"); v != "" {
		cfg.Search.SearxngURL = v
	}
	return cfg, nil
}

// Save 将配置写入程序同目录下的 config.json。
func (c *Config) Save() error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}
