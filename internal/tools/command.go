package tools

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// 危险命令黑名单（支持正则）。命中即拒绝执行。
var blacklistPatterns = []string{
	`(?i)\brm\s+-[rf]`,                 // rm -rf / 删除根/强制删除
	`(?i)\brmdir\s+/[s]`,               // 强制删除目录树
	`(?i)\bformat\s+[a-z]:`,            // format C:
	`(?i)\bmkfs`,                       // 格式化文件系统
	`(?i)\bshutdown`,                   // 关机
	`(?i)\breboot`,                     // 重启
	`(?i)\bdd\s+if=`,                   // dd 磁盘复制
	`(?i)\bdeltree`,                    // 删除目录树
	`(?i)\bdiskpart`,                   // 磁盘分区
	`:\(\)`,                            // fork 炸弹
	`(?i)\bRemove-Item\b.*\b-Recurse\b`, // PowerShell 递归删除
	`(?i)\bRemove-Item\b.*\b-Force\b`,   // PowerShell 强制删除（可删单文件，危险）
	`(?i)\bStop-Computer\b`,             // PowerShell 关机
	`(?i)\bRestart-Computer\b`,          // PowerShell 重启
	`(?i)\bFormat-Volume\b`,             // PowerShell 格式化卷
	`(?i)\bClear-Disk\b`,                // PowerShell 清除磁盘
}

var blacklistRe = compileBlacklist()

func compileBlacklist() []*regexp.Regexp {
	res := make([]*regexp.Regexp, 0, len(blacklistPatterns))
	for _, p := range blacklistPatterns {
		re, err := regexp.Compile(p)
		if err == nil {
			res = append(res, re)
		}
	}
	return res
}

// ExecCommandTool 执行 shell 命令。
type ExecCommandTool struct {
	// OnOutput 可选：逐行实时输出回调（GUI/CLI 展示）。
	OnOutput func(line string)
}

func (t *ExecCommandTool) Name() string { return "exec_command" }
func (t *ExecCommandTool) Description() string {
	return "执行 shell 命令并返回输出。Windows 下使用 PowerShell 解释（支持 $变量、管道、别名、对象管道），Unix 下使用 /bin/sh。默认前台等待命令结束；background=true 时后台执行并立即返回任务 ID（可用 check_command_status 查询状态）。危险命令（格式化、删除、关机等）会被拦截。"
}
func (t *ExecCommandTool) ArgsSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command":     map[string]any{"type": "string", "description": "要执行的命令"},
			"timeout_sec": map[string]any{"type": "integer", "description": "超时秒数，前台默认 60，后台默认 600"},
			"cwd":         map[string]any{"type": "string", "description": "工作目录，默认当前目录"},
			"background":  map[string]any{"type": "boolean", "description": "是否后台执行（默认 false）"},
		},
		"required": []string{"command"},
	}
}

const (
	maxOutputLines = 2000 // 最多保留的输出行数，防止溢出
	defaultTimeout = 60 * time.Second
	defaultBgTimeout = 600 * time.Second // 后台任务的默认超时
)

func (t *ExecCommandTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	cmdStr, _ := args["command"].(string)
	cmdStr = strings.TrimSpace(cmdStr)
	if cmdStr == "" {
		return "", fmt.Errorf("命令不能为空")
	}

	// 1. 黑名单检查
	for _, re := range blacklistRe {
		if re.MatchString(cmdStr) {
			return "", fmt.Errorf("命令被安全策略拦截（命中黑名单 %q）：%s", re.String(), cmdStr)
		}
	}

	cwd, _ := args["cwd"].(string)

	// 2. 后台模式：不绑定调用方 context（避免回合结束时被取消），
	//    使用独立超时；任务结束时会释放定时器。
	if background, _ := args["background"].(bool); background {
		bgTimeout := defaultBgTimeout
		if sec, ok := args["timeout_sec"].(float64); ok && sec > 0 {
			bgTimeout = time.Duration(sec) * time.Second
		}
		bgCtx, cancel := context.WithTimeout(context.Background(), bgTimeout)
		id, err := bg.start(bgCtx, cancel, cmdStr, cwd)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("已在后台启动任务 %s。可用 check_command_status 查询状态（参数 task_id: %s）。", id, id), nil
	}

	// 3. 前台模式：超时并等待命令结束
	timeout := defaultTimeout
	if sec, ok := args["timeout_sec"].(float64); ok && sec > 0 {
		timeout = time.Duration(sec) * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 4. 执行（Windows 用 PowerShell 解释以支持 $变量等语法，配合黑名单防护）
	//    注入 UTF-8 输入/输出编码，避免命令输出中文被系统代码页误解码成乱码。
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		pre := "[Console]::OutputEncoding=[Text.Encoding]::UTF8; $OutputEncoding=[Text.Encoding]::UTF8; "
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", pre+cmdStr)
	} else {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", cmdStr)
	}
	if cwd != "" {
		cmd.Dir = cwd
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	cmd.Stderr = cmd.Stdout // 合并 stderr 到 stdout

	if err := cmd.Start(); err != nil {
		return "", err
	}

	// 4. 逐行读取输出（GBK/UTF-8 自动识别转码）
	var out strings.Builder
	lineCount := 0
	truncated := false
	scanner := cmdScanner(stdout)
	for scanner.Scan() {
		line := decodeCmdLine(scanner.Text())
		if t.OnOutput != nil {
			t.OnOutput(line)
		}
		if lineCount < maxOutputLines {
			out.WriteString(line + "\n")
			lineCount++
		} else {
			truncated = true
		}
	}

	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return "", fmt.Errorf("命令执行被取消: %v", ctx.Err())
	}

	if truncated {
		out.WriteString(fmt.Sprintf("...(输出超过 %d 行已截断)\n", maxOutputLines))
	}
	result := out.String()
	if waitErr != nil {
		if result != "" {
			result = strings.TrimSpace(result) + "\n"
		}
		result += fmt.Sprintf("[命令退出错误: %v]", waitErr)
	}
	return truncate(result, maxToolOutput), nil
}
