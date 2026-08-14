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

// Record 是一条会话记录：消息历史 + 该会话关联的工作目录。
type Record struct {
	// WorkingDir 会话的工作目录（绝对路径，Agent 相对路径的基准）；空表示使用进程当前目录。
	WorkingDir string `json:"working_dir,omitempty"`
	// Messages 会话消息历史。
	Messages []llm.Message `json:"messages"`
}

// Save 将会话记录保存到 <dir>/<名称>.json。
// name 为空时用 NameFor 生成（工作目录末级目录名，冲突时加序号，无工作目录回退时间戳）。
// 返回保存的文件路径。
func Save(dir string, name string, rec Record) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if name == "" {
		name = NameFor(dir, rec)
	}
	p := filepath.Join(dir, name+".json")
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return "", err
	}
	return p, nil
}

// NameFor 为会话生成名称：优先用工作目录的最后一级目录名（如 D:\work\proj → "proj"）；
// 同名文件已存在时追加序号（proj、proj-2、proj-3…）避免覆盖；无工作目录时回退为时间戳。
func NameFor(dir string, rec Record) string {
	base := ""
	if rec.WorkingDir != "" {
		b := filepath.Base(filepath.Clean(rec.WorkingDir))
		if b != "." && b != string(filepath.Separator) && b != "" {
			base = b
		}
	}
	if base == "" {
		return time.Now().Format("2006-01-02_15-04-05")
	}
	if !fileExists(filepath.Join(dir, base+".json")) {
		return base
	}
	for i := 2; ; i++ {
		n := fmt.Sprintf("%s-%d", base, i)
		if !fileExists(filepath.Join(dir, n+".json")) {
			return n
		}
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
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

// Load 加载指定会话文件。
// 兼容旧格式：内容为 JSON 数组（旧版仅存消息列表）时按消息数组解析，工作目录为空。
func Load(dir, name string) (Record, error) {
	var rec Record
	p := filepath.Join(dir, name)
	if filepath.Ext(p) == "" {
		p += ".json"
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return rec, err
	}
	if isJSONArray(data) {
		var msgs []llm.Message
		if err := json.Unmarshal(data, &msgs); err != nil {
			return rec, err
		}
		rec.Messages = msgs
		return rec, nil
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, err
	}
	return rec, nil
}

// isJSONArray 判断数据是否为 JSON 数组（跳过前导空白）。
func isJSONArray(data []byte) bool {
	for _, b := range data {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '[':
			return true
		default:
			return false
		}
	}
	return false
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
