// Package session 提供会话历史的保存与恢复。
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"go-agent/internal/llm"
)

// Save 将会话消息保存到 <dir>/<名称>.json。
// name 为空时使用时间戳命名。
func Save(dir string, name string, messages []llm.Message) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if name == "" {
		name = time.Now().Format("2006-01-02_15-04-05")
	}
	p := filepath.Join(dir, name+".json")
	data, err := json.MarshalIndent(messages, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return "", err
	}
	return p, nil
}

// List 列出目录下所有会话文件名（不含扩展名），按修改时间倒序。
func List(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) == ".json" {
			names = append(names, filepath.Base(e.Name()))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names, nil
}

// Load 加载指定会话文件的消息。
func Load(dir, name string) ([]llm.Message, error) {
	p := filepath.Join(dir, name)
	if filepath.Ext(p) == "" {
		p += ".json"
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var messages []llm.Message
	if err := json.Unmarshal(data, &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

// Describe 生成会话列表的展示文本。
func Describe(names []string) string {
	if len(names) == 0 {
		return "暂无已保存的会话"
	}
	out := "已保存的会话：\n"
	for i, n := range names {
		out += fmt.Sprintf("  %d. %s\n", i+1, n)
	}
	return out
}
