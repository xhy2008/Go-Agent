// Package logx 提供统一的日志与状态上报：
// slog 结构化日志输出到 stderr（控制台日志），同时通过监听器同步到 GUI 状态栏。
// 所有错误、异常与 fallback 降级都通过本包上报，确保任何异常在两处均可见。
package logx

import (
	"fmt"
	"log/slog"
	"os"
	"sync"
)

var (
	mu       sync.Mutex
	listener func(level, msg string)
	logger   = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
)

// SetListener 注册状态监听器（GUI 状态栏）。收到 (level, msg)，
// level 取值：info / warn / error。
func SetListener(f func(level, msg string)) {
	mu.Lock()
	listener = f
	mu.Unlock()
}

// SetLogLevel 调整日志级别（调试时设为 slog.LevelDebug）。
func SetLogLevel(l slog.Level) {
	logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

func notify(level, msg string) {
	mu.Lock()
	f := listener
	mu.Unlock()
	if f != nil {
		f(level, msg)
	}
}

// Debug 记录调试信息（仅控制台）。
func Debug(format string, args ...any) {
	logger.Debug(fmt.Sprintf(format, args...))
}

// Info 记录常规信息。
func Info(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	logger.Info(msg)
	notify("info", msg)
}

// Warn 记录警告（含 fallback 降级等异常但可恢复的状态）。
func Warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	logger.Warn(msg)
	notify("warn", msg)
}

// Error 记录错误。
func Error(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	logger.Error(msg)
	notify("error", msg)
}
