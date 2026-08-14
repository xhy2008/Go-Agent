package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"go-agent/internal/logx"
)

// 超时设计：建连/握手/响应头用短超时（http 超时），流式 body 阶段不受总超时限制，
// 改为独立的“空闲超时看门狗”检测断网/卡死，避免长输出被一刀切总超时误杀。
const (
	// transportTimeout 建连、TLS 握手、等待服务端响应头的超时（http 超时）。
	transportTimeout = 3 * time.Second
	// firstByteTimeout 等待首个数据块的最长时间（覆盖首 token 延迟）。
	firstByteTimeout = 60 * time.Second
	// interChunkTimeout 相邻数据块之间的最大空闲间隔：连续超过该时长无数据即判定连接中断。
	interChunkTimeout = 3 * time.Second
)

// ErrStreamInterrupted 标记流式输出在收到 SSE 结束标记 [DONE] 之前中断
// （断网、空闲超时、服务端提前断开）。调用方可 errors.Is 判断后决定是否保留已累积的部分内容。
var ErrStreamInterrupted = errors.New("流式输出中断")

// Client 是 OpenAI 兼容 chat/completions 接口的客户端，支持 SSE 流式响应。
type Client struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

// NewClient 创建一个客户端。baseURL 为空时默认 DeepSeek 官方地址。
func NewClient(baseURL, apiKey, model string) *Client {
	if baseURL == "" {
		baseURL = "https://api.deepseek.com"
	}
	if model == "" {
		model = "deepseek-chat"
	}
	// 不设 Client.Timeout（总超时会误杀长流），连接阶段的超时交给 Transport。
	transport := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: transportTimeout}).DialContext,
		TLSHandshakeTimeout: transportTimeout,
		ResponseHeaderTimeout: transportTimeout,
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Transport: transport},
	}
}

// Model 返回当前使用的模型名。
func (c *Client) Model() string { return c.model }

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []ToolDef `json:"tools,omitempty"`
	Stream   bool      `json:"stream"`
}

type chatChunk struct {
	Choices []struct {
		Delta struct {
			Content           string     `json:"content"`
			ReasoningContent  string     `json:"reasoning_content"` // DeepSeek 思考模式：推理文本增量
			ToolCalls         []ToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *usageInfo `json:"usage"` // 仅流式响应末尾 chunk 携带
}

// usageInfo 是流式响应末尾 chunk 的真实用量。
// DeepSeek 会拆分缓存命中/未命中 token，用于验证上下文缓存命中率与费用监控。
type usageInfo struct {
	PromptTokens          int `json:"prompt_tokens"`
	CompletionTokens      int `json:"completion_tokens"`
	TotalTokens           int `json:"total_tokens"`
	PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"`
}

// Chat 调用模型完成一次对话。
// onContent 收到流式文本增量（可为 nil）；onReasoning 收到思考模式的推理文本增量
// （可为 nil；DeepSeek V4 思考模式时非空）；onUsage 在收到末尾 chunk 的用量后回调
// （hit/miss 为服务端报告的缓存命中/未命中 token 数，可为 nil）。
// 返回完整的 assistant 消息（含 ReasoningContent 与 ToolCalls）；若模型发起工具调用则填充 ToolCalls。
func (c *Client) Chat(ctx context.Context, messages []Message, tools []ToolDef, onContent func(string), onReasoning func(string), onUsage func(hit, miss int)) (Message, error) {
	reqBody := chatRequest{
		Model:    c.model,
		Messages: messages,
		Tools:    tools,
		Stream:   true,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return Message{}, err
	}

	// 请求级 context 由本函数持有：空闲超时看门狗通过取消它来中断阻塞中的流式读取。
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Message{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return Message{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Message{}, fmt.Errorf("llm api error %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	return c.readStream(ctx, reqCtx, cancel, resp.Body, onContent, onReasoning, onUsage)
}

// readStream 解析 SSE 流，累积推理文本、正文内容与工具调用。
// 出错（断网/空闲超时/提前关闭）时已累积的部分内容仍随 Message 返回，
// 供上层保留，避免已生成的输出丢失。
func (c *Client) readStream(ctx context.Context, reqCtx context.Context, cancel context.CancelFunc, r io.Reader, onContent func(string), onReasoning func(string), onUsage func(hit, miss int)) (Message, error) {
	msg := Message{Role: "assistant"}
	var content strings.Builder
	var reasoning strings.Builder
	var toolCalls []*ToolCall // 按下标累积流式增量

	// 空闲超时看门狗：任何数据到达都视为活跃；连续无数据超过预算即判定断网。
	// 预算分两段：收到首块前用 firstByteTimeout（覆盖首 token 延迟），
	// 收到首块后收紧为 interChunkTimeout（断网后快速失败）。
	// 注意必须取消 reqCtx（发起请求的 context）才能真正中断阻塞中的 body 读取，
	// 取消派生出的子 context 不会传到 http.Transport。
	var gotData, idleFired atomic.Bool
	var lastSeen atomic.Int64
	lastSeen.Store(time.Now().UnixNano())
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-reqCtx.Done():
				return // 请求结束（正常或取消），退出看门狗
			case <-ticker.C:
				budget := firstByteTimeout
				if gotData.Load() {
					budget = interChunkTimeout
				}
				if time.Since(time.Unix(0, lastSeen.Load())) > budget {
					idleFired.Store(true)
					cancel()
					return
				}
			}
		}
	}()

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	done := false // 是否收到 SSE 结束标记 [DONE]
	var usage *usageInfo
	for scanner.Scan() {
		lastSeen.Store(time.Now().UnixNano()) // 有数据即活跃（含心跳/注释行）
		gotData.Store(true)
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if data == "[DONE]" {
			done = true
			break
		}
		var chunk chatChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // 忽略无法解析的行
		}
		if chunk.Usage != nil {
			usage = chunk.Usage // 用量出现在末尾 chunk，取最后一次
		}
		for _, ch := range chunk.Choices {
			if d := ch.Delta.ReasoningContent; d != "" {
				reasoning.WriteString(d)
				if onReasoning != nil {
					onReasoning(d)
				}
			}
			if d := ch.Delta.Content; d != "" {
				content.WriteString(d)
				if onContent != nil {
					onContent(d)
				}
			}
			for _, tc := range ch.Delta.ToolCalls {
				for len(toolCalls) <= tc.Index {
					toolCalls = append(toolCalls, &ToolCall{Type: "function"})
				}
				t := toolCalls[tc.Index]
				if tc.ID != "" {
					t.ID = tc.ID
				}
				if tc.Function.Name != "" {
					t.Function.Name += tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					t.Function.Arguments += tc.Function.Arguments
				}
			}
		}
	}
	msg.Content = content.String()
	msg.ReasoningContent = reasoning.String()
	for _, tc := range toolCalls {
		if tc.ID == "" {
			tc.ID = fmt.Sprintf("call_%d", time.Now().UnixNano())
		}
		msg.ToolCalls = append(msg.ToolCalls, *tc)
	}

	// 用户主动取消优先于其它错误，保证停止按钮/Ctrl+C 仍显示“已停止”。
	// 取消请求会中断阻塞中的 body 读取使循环退出，此时 msg.Content 已带部分内容。
	if ctx.Err() != nil {
		return msg, ctx.Err()
	}
	if err := scanner.Err(); err != nil {
		if idleFired.Load() {
			return msg, fmt.Errorf("%w: 连接空闲超过 %.0f 秒无数据，疑似断网", ErrStreamInterrupted, interChunkTimeout.Seconds())
		}
		return msg, fmt.Errorf("%w: %v", ErrStreamInterrupted, err)
	}
	if !done {
		// 服务端正常关闭但未发送 [DONE]：静默截断，同样视为中断，避免截断内容被当作完整回复。
		return msg, fmt.Errorf("%w: 连接在收到结束标记前关闭", ErrStreamInterrupted)
	}
	if usage != nil {
		logx.Info("LLM 用量: 输入 %d（缓存命中 %d / 未命中 %d），输出 %d，总计 %d",
			usage.PromptTokens, usage.PromptCacheHitTokens, usage.PromptCacheMissTokens,
			usage.CompletionTokens, usage.TotalTokens)
		if onUsage != nil {
			onUsage(usage.PromptCacheHitTokens, usage.PromptCacheMissTokens)
		}
	}
	return msg, nil
}
