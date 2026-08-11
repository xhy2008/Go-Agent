package tools

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

const maxJobLines = 200 // 后台任务最多保留的输出行数

// bgManager 管理 exec_command 的后台任务。
type bgManager struct {
	mu   sync.Mutex
	jobs map[string]*bgJob
	seq  int
}

type bgJob struct {
	id       string
	done     chan struct{}
	cancel   context.CancelFunc
	mu       sync.Mutex
	running  bool
	exitCode int
	errMsg   string
	lines    []string // 最近输出（环形保留）
}

func (j *bgJob) addLine(line string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.lines = append(j.lines, line)
	if len(j.lines) > maxJobLines {
		// 丢弃最旧的一半，保留最近输出
		j.lines = append([]string(nil), j.lines[len(j.lines)/2:]...)
	}
}

// snapshot 返回任务当前状态的只读副本。
type bgJobSnapshot struct {
	ID       string
	Running  bool
	ExitCode int
	Err      string
	Output   []string
}

func (s bgJobSnapshot) String() string {
	var b strings.Builder
	switch {
	case s.Running:
		fmt.Fprintf(&b, "任务 %s 正在运行中\n", s.ID)
	case s.ExitCode == 0:
		fmt.Fprintf(&b, "任务 %s 已完成，退出码 0\n", s.ID)
	default:
		fmt.Fprintf(&b, "任务 %s 已结束，退出码 %d\n", s.ID, s.ExitCode)
	}
	if s.Err != "" {
		fmt.Fprintf(&b, "错误: %s\n", s.Err)
	}
	fmt.Fprintf(&b, "最近输出 (%d 行):\n", len(s.Output))
	for _, l := range s.Output {
		b.WriteString(l + "\n")
	}
	return b.String()
}

var bg = &bgManager{jobs: make(map[string]*bgJob)}

// start 启动后台任务并返回任务 ID。ctx 由调用方持有，任务结束时 cancel 会被调用。
func (m *bgManager) start(ctx context.Context, cancel context.CancelFunc, cmdStr, cwd string) (string, error) {
	m.mu.Lock()
	m.seq++
	id := fmt.Sprintf("bg%d", m.seq)
	m.mu.Unlock()

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

	job := &bgJob{id: id, running: true, done: make(chan struct{}), cancel: cancel}
	m.mu.Lock()
	m.jobs[id] = job
	m.mu.Unlock()

	go func() {
		defer close(job.done)
		scanner := cmdScanner(stdout)
		for scanner.Scan() {
			job.addLine(decodeCmdLine(scanner.Text()))
		}
		waitErr := cmd.Wait()
		job.mu.Lock()
		job.running = false
		if waitErr != nil {
			if ee, ok := waitErr.(*exec.ExitError); ok {
				job.exitCode = ee.ExitCode()
			} else {
				job.errMsg = waitErr.Error()
			}
		}
		job.mu.Unlock()
		job.cancel() // 提前释放超时定时器
	}()
	return id, nil
}

// status 返回任务状态快照；任务不存在时报错。
func (m *bgManager) status(id string) (bgJobSnapshot, error) {
	m.mu.Lock()
	job, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return bgJobSnapshot{}, fmt.Errorf("任务 %s 不存在", id)
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	return bgJobSnapshot{
		ID:       job.id,
		Running:  job.running,
		ExitCode: job.exitCode,
		Err:      job.errMsg,
		Output:   append([]string(nil), job.lines...),
	}, nil
}

// CheckCommandStatusTool 查询后台命令的执行状态与最近输出。
type CheckCommandStatusTool struct{}

func (t *CheckCommandStatusTool) Name() string { return "check_command_status" }
func (t *CheckCommandStatusTool) Description() string {
	return "检查 exec_command 后台任务（background=true）的执行状态与最近输出。任务运行中返回实时进度；结束后返回退出码和最终输出。"
}
func (t *CheckCommandStatusTool) ArgsSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task_id": map[string]any{"type": "string", "description": "exec_command 后台任务返回的任务 ID，如 bg1"},
		},
		"required": []string{"task_id"},
	}
}

func (t *CheckCommandStatusTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	id, _ := args["task_id"].(string)
	if id == "" {
		return "", fmt.Errorf("task_id 不能为空")
	}
	snap, err := bg.status(id)
	if err != nil {
		return "", err
	}
	return truncate(snap.String(), maxToolOutput), nil
}
