package tools

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// sensitiveEnvRe 匹配含敏感信息的环境变量名（key/token/secret/password 等）。
// 要求关键词前后有 _ - . 或字符串边界，避免误伤如 HOCKEYSTICK、MONKEY 等普通名称。
var sensitiveEnvRe = regexp.MustCompile(`(?i)(^|[_\-.])(key|token|secret|passwd|password|credential|auth)([_\-.]|$)`)

// isSensitiveEnv 判断环境变量名是否含敏感信息。
func isSensitiveEnv(name string) bool { return sensitiveEnvRe.MatchString(name) }

// GetEnvTool 查询环境变量；name 为空时列出全部（敏感变量只显示名称，隐藏值）。
type GetEnvTool struct{}

func (t *GetEnvTool) Name() string { return "get_env" }
func (t *GetEnvTool) Description() string {
	return "查询环境变量。name 指定时返回该变量的值；name 为空时列出全部变量，含 key/token/secret/password 等敏感关键词的变量只显示名称、值以 <已隐藏> 代替，避免密钥泄漏进上下文。"
}
func (t *GetEnvTool) ArgsSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "description": "环境变量名（可选，为空时列出全部）"},
		},
	}
}

func (t *GetEnvTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	name, _ := args["name"].(string)
	name = strings.TrimSpace(name)
	if name != "" {
		if v, ok := os.LookupEnv(name); ok {
			return name + "=" + v, nil
		}
		return fmt.Sprintf("环境变量 %q 未设置", name), nil
	}

	env := os.Environ()
	sort.Strings(env)
	var b strings.Builder
	for _, e := range env {
		if b.Len() >= maxToolOutput {
			b.WriteString("\n...(输出过长，已截断)")
			break
		}
		n, v, _ := strings.Cut(e, "=")
		if isSensitiveEnv(n) {
			v = "<已隐藏>"
		}
		fmt.Fprintf(&b, "%s=%s\n", n, v)
	}
	return strings.TrimSpace(b.String()), nil
}
