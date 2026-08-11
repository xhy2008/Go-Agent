package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultTimeout = 5 * time.Minute

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
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Timeout: defaultTimeout},
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
			Content   string     `json:"content"`
			ToolCalls []ToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// Chat 调用模型完成一次对话。
// onContent 收到流式文本增量（可为 nil）。
// 返回完整的 assistant 消息；若模型发起工具调用则填充 ToolCalls。
func (c *Client) Chat(ctx context.Context, messages []Message, tools []ToolDef, onContent func(string)) (Message, error) {
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

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
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

	return c.readStream(ctx, resp.Body, onContent)
}

// readStream 解析 SSE 流，累积文本内容与工具调用。
func (c *Client) readStream(ctx context.Context, r io.Reader, onContent func(string)) (Message, error) {
	msg := Message{Role: "assistant"}
	var content strings.Builder
	var toolCalls []*ToolCall // 按下标累积流式增量

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return Message{}, ctx.Err()
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if data == "[DONE]" {
			break
		}
		var chunk chatChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // 忽略无法解析的行
		}
		for _, ch := range chunk.Choices {
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
	if err := scanner.Err(); err != nil {
		return Message{}, err
	}

	msg.Content = content.String()
	for _, tc := range toolCalls {
		if tc.ID == "" {
			tc.ID = fmt.Sprintf("call_%d", time.Now().UnixNano())
		}
		msg.ToolCalls = append(msg.ToolCalls, *tc)
	}
	return msg, nil
}
