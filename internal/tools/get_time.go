package tools

import (
	"context"
	"fmt"
	"time"
)

// GetCurrentTimeTool 返回当前本地时间与时区信息。
type GetCurrentTimeTool struct{}

func (t *GetCurrentTimeTool) Name() string { return "get_current_time" }
func (t *GetCurrentTimeTool) Description() string {
	return "返回当前本地时间、时区与 UTC 时间。用于时间敏感的任务（文件命名、日期计算、日志分析、截止时间判断）。"
}
func (t *GetCurrentTimeTool) ArgsSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func (t *GetCurrentTimeTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	now := time.Now()
	zone, offset := now.Zone()
	return fmt.Sprintf("本地时间: %s\n时区: %s (UTC%+02d)\nUTC 时间: %s",
		now.Format("2006-01-02 15:04:05"), zone, offset/3600, now.UTC().Format("2006-01-02 15:04:05")), nil
}
