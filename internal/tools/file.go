package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxToolOutput = 12000 // 单次工具返回给 LLM 的最大字符数

// truncate 截断过长输出，避免撑爆上下文。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n...(输出过长，已截断)"
}

// ReadFileRangeTool 读取文件的指定行范围。
type ReadFileRangeTool struct{}

func (t *ReadFileRangeTool) Name() string { return "read_file_range" }
func (t *ReadFileRangeTool) Description() string {
	return "读取文件：start_line 为正数时从文件头第 N 行开始读；为负数时读取文件末尾 |N| 行；start_line 与 end_line 都为 0 时读取整个文件。行号从 1 开始。适用于查看代码文件。"
}
func (t *ReadFileRangeTool) ArgsSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "文件路径（相对或绝对）"},
			"start_line": map[string]any{
				"type":        "integer",
				"description": "起始行号：正数从文件头第 N 行开始；负数读取文件末尾 |N| 行；0 且 end_line 也为 0 时读取整个文件",
			},
			"end_line": map[string]any{
				"type":        "integer",
				"description": "结束行号（1 起含该行），<=0 表示读到末尾；start_line 为负数时忽略",
			},
		},
		"required": []string{"path"},
	}
}

func (t *ReadFileRangeTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	path, _ := args["path"].(string)
	abs, err := safePath(path)
	if err != nil {
		return "", err
	}
	start, _ := args["start_line"].(float64)
	end, _ := args["end_line"].(float64)
	startLine := int(start)
	endLine := int(end)

	f, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// 模式一：start_line < 0 → 读取文件末尾 |start_line| 行（end_line 忽略）。
	if startLine < 0 {
		n := -startLine
		type ln struct {
			no   int
			text string
		}
		buf := make([]ln, n) // 环形缓冲，只保留最后 n 行
		used := 0
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			used++
			buf[used%n] = ln{no: used, text: scanner.Text()}
		}
		if err := scanner.Err(); err != nil {
			return "", err
		}
		if used == 0 {
			return "", fmt.Errorf("文件 %s 为空", abs)
		}
		var out strings.Builder
		begin := 0
		if used > n {
			begin = used - n
		}
		for i := begin; i < used; i++ {
			l := buf[i%n]
			out.WriteString(fmt.Sprintf("%d\t%s\n", l.no, l.text))
		}
		return truncate(out.String(), maxToolOutput), nil
	}

	// 模式二：start_line == 0 且 end_line == 0 → 整个文件；单个 0 视作从文件头开始。
	if startLine == 0 {
		startLine = 1
	}
	if endLine > 0 && endLine < startLine {
		return "", fmt.Errorf("end_line(%d) 不能小于 start_line(%d)", endLine, startLine)
	}
	fileLines := endLine > 0

	var out strings.Builder
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 0
	lineCount := 0
	for scanner.Scan() {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		lineNo++
		if lineNo < startLine {
			continue
		}
		if fileLines && lineNo > endLine {
			break
		}
		lineCount++
		out.WriteString(fmt.Sprintf("%d\t%s\n", lineNo, scanner.Text()))
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if lineCount == 0 {
		if fileLines {
			return "", fmt.Errorf("文件 %s 没有行号在 %d-%d 范围内的内容", abs, startLine, endLine)
		}
		return "", fmt.Errorf("文件 %s 为空或没有行号在 %d 之后的内容", abs, startLine)
	}
	return truncate(out.String(), maxToolOutput), nil
}

// WriteFileTool 覆盖或追加写入文件。
type WriteFileTool struct{}

func (t *WriteFileTool) Name() string { return "write_file" }
func (t *WriteFileTool) Description() string {
	return "写入文件。append 为 true 时在文件末尾追加，否则覆盖整个文件。会自动创建父目录。"
}
func (t *WriteFileTool) ArgsSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string", "description": "文件路径"},
			"content": map[string]any{"type": "string", "description": "要写入的内容"},
			"append":  map[string]any{"type": "boolean", "description": "是否追加（默认 false 覆盖）"},
		},
		"required": []string{"path", "content"},
	}
}

func (t *WriteFileTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)
	appendMode, _ := args["append"].(bool)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}

	abs, err := safePath(path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	flag := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(abs, flag, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return "", err
	}
	TrackModified(abs)
	action := "覆盖写入"
	if appendMode {
		action = "追加写入"
	}
	return fmt.Sprintf("已%s %s（%d 字节）", action, abs, len(content)), nil
}
