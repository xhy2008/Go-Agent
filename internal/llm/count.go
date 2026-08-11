package llm

import (
	"github.com/pkoukk/tiktoken-go"

	"go-agent/internal/logx"
)

// enc 是懒加载的 BPE 编码器；加载失败时为 nil，走近似计数。
var enc *tiktoken.Tiktoken

func init() {
	tke, err := tiktoken.GetEncoding("cl100k_base")
	if err == nil {
		enc = tke
	} else {
		logx.Warn("tiktoken 初始化失败（%v），token 计数降级为近似估算", err)
	}
}

// CountTokens 估算文本 token 数。
// 优先使用 tiktoken（cl100k_base）；不可用时使用近似值（ASCII 4 字符≈1 token，中文约 0.6 token/字）。
func CountTokens(text string) int {
	if enc != nil {
		toks := enc.Encode(text, nil, nil)
		return len(toks)
	}
	return approximateTokens(text)
}

// CountMessages 统计消息列表的总 token 数。
func CountMessages(messages []Message) int {
	total := 0
	for _, m := range messages {
		total += CountTokens(m.Content)
		for _, tc := range m.ToolCalls {
			total += CountTokens(tc.Function.Name) + CountTokens(tc.Function.Arguments)
		}
	}
	return total
}

// approximateTokens 粗略估算 token 数（tiktoken 不可用时兜底）。
func approximateTokens(s string) int {
	var ascii, cjk int
	for _, r := range s {
		if r < 128 {
			ascii++
		} else {
			cjk++
		}
	}
	return ascii/4 + int(float64(cjk)*0.6)
}
