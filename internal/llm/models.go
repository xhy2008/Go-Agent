package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ModelInfo 描述服务端可用模型。Context 为上下文窗口 token 数，
// 仅当服务在 /models 响应中提供该字段时非 0（DeepSeek 不提供，为 0）。
type ModelInfo struct {
	ID      string `json:"id"`
	Context int    `json:"context,omitempty"`
}

type modelsResp struct {
	Data []struct {
		ID      string `json:"id"`
		Context int    `json:"context"`
	} `json:"data"`
}

// Models 动态获取当前服务可用的模型列表（GET /models，OpenAI 兼容）。
func (c *Client) Models(ctx context.Context) ([]ModelInfo, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("models api error %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var mr modelsResp
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, err
	}
	list := make([]ModelInfo, 0, len(mr.Data))
	for _, d := range mr.Data {
		list = append(list, ModelInfo{ID: d.ID, Context: d.Context})
	}
	return list, nil
}

// knownWindows 内置模型→上下文窗口映射（token 数）。
// DeepSeek /models 接口不返回上下文长度，官方文档明确 V4 系列为 1M，
// 这里作为 API 不提供该字段时的兜底；未知模型由调用方决定默认值。
var knownWindows = map[string]int{
	"deepseek-v4-flash": 1000000,
	"deepseek-v4-pro":   1000000,
	// 以下为已弃用的旧模型别名（2026/07/24 起映射到 V4 系列对应模式）
	"deepseek-chat":     1000000,
	"deepseek-reasoner": 1000000,
}

// ContextWindowFor 返回已知模型的上下文窗口 token 数；未知模型返回 0。
func ContextWindowFor(model string) int {
	if v, ok := knownWindows[strings.ToLower(strings.TrimSpace(model))]; ok {
		return v
	}
	return 0
}

// probeTimeout 探测模型列表的超时时间，避免拖慢启动。
const probeTimeout = 10 * time.Second

// ProbeTimeout 返回模型列表探测的推荐超时时间。
func ProbeTimeout() time.Duration { return probeTimeout }
