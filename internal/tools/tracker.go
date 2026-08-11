package tools

import "sync"

// 文件修改跟踪：write_file / edit_file 成功后记录路径，供 GUI 侧边栏展示。
var (
	modifiedMu     sync.Mutex
	modifiedFiles  []string
)

// TrackModified 记录一个被修改的文件路径（去重）。
func TrackModified(path string) {
	modifiedMu.Lock()
	defer modifiedMu.Unlock()
	for _, p := range modifiedFiles {
		if p == path {
			return
		}
	}
	modifiedFiles = append(modifiedFiles, path)
}

// ModifiedFiles 返回本次运行中修改过的文件列表。
func ModifiedFiles() []string {
	modifiedMu.Lock()
	defer modifiedMu.Unlock()
	out := make([]string, len(modifiedFiles))
	copy(out, modifiedFiles)
	return out
}

// ResetModified 清空修改记录（/clear 或新会话时调用）。
func ResetModified() {
	modifiedMu.Lock()
	defer modifiedMu.Unlock()
	modifiedFiles = nil
}
