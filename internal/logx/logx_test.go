package logx

import (
	"strings"
	"sync"
	"testing"
)

func TestListener(t *testing.T) {
	var mu sync.Mutex
	var got []string
	SetListener(func(level, msg string) {
		mu.Lock()
		got = append(got, level+":"+msg)
		mu.Unlock()
	})

	Warn("测试警告 %d", 1)
	Error("测试错误 %s", "x")

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("expected 2 notifications, got %v", got)
	}
	if !strings.Contains(got[0], "warn:测试警告 1") {
		t.Errorf("unexpected warn: %s", got[0])
	}
	if !strings.Contains(got[1], "error:测试错误 x") {
		t.Errorf("unexpected error: %s", got[1])
	}
}
