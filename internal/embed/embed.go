// Package embed 提供基于 llama.cpp 的本地 embedding：加载 GGUF 模型、
// 文本向量化与余弦相似度计算。llama.cpp 以动态库（llama_bridge.dll）形式
// 通过 syscall 按需加载——仅当配置了 embedding 模型时才加载 DLL，否则完全不
// 依赖 llama.cpp，回退 FTS5 全文检索（原版 codegraph 方案）。
package embed

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

const bridgeDLL = "llama_bridge.dll"

// bridge 是 llama_bridge.dll 的延迟加载句柄（仅 Load 时初始化）。
type bridge struct {
	dll  *syscall.LazyDLL
	new  *syscall.LazyProc // cg_embed_new(const char* path, int pooling) -> handle
	dim  *syscall.LazyProc // cg_embed_dim(handle) -> int
	err  *syscall.LazyProc // cg_embed_error(handle, char* buf, int buflen) -> int
	enc  *syscall.LazyProc // cg_embed_encode(handle, text, float* out, int maxlen) -> int
	free *syscall.LazyProc // cg_embed_free(handle)
}

var (
	loadOnce sync.Once
	loadErr  error
	br       *bridge
)

// loadBridge 首次调用时定位并加载 llama_bridge.dll。
func loadBridge() error {
	loadOnce.Do(func() {
		path, err := findBridgeDLL()
		if err != nil {
			loadErr = err
			return
		}
		dll := syscall.NewLazyDLL(path)
		if err := dll.Load(); err != nil {
			loadErr = fmt.Errorf("embed: 加载 %s 失败: %w", bridgeDLL, err)
			return
		}
		br = &bridge{
			dll:  dll,
			new:  dll.NewProc("cg_embed_new"),
			dim:  dll.NewProc("cg_embed_dim"),
			err:  dll.NewProc("cg_embed_error"),
			enc:  dll.NewProc("cg_embed_encode"),
			free: dll.NewProc("cg_embed_free"),
		}
	})
	return loadErr
}

// findBridgeDLL 定位 llama_bridge.dll：优先 exe 目录与工作目录，
// 开发期回退到仓库 third_party/llama-bridge/。
func findBridgeDLL() (string, error) {
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), bridgeDLL))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, bridgeDLL))
	}
	if _, file, _, ok := runtime.Caller(0); ok {
		root := filepath.Dir(filepath.Dir(filepath.Dir(file))) // internal/embed -> internal -> repo
		candidates = append(candidates, filepath.Join(root, "third_party", "llama-bridge", bridgeDLL))
	}
	for _, c := range candidates {
		if fileExists(c) {
			return c, nil
		}
	}
	return "", fmt.Errorf("embed: 未找到 %s（先构建桥接层：third_party/llama-bridge/build.ps1）", bridgeDLL)
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// Model 是已加载的 embedding 模型（线程安全）。
type Model struct {
	mu      sync.Mutex
	handle  uintptr
	nEmbd   int
	pooling Pooling
}

// Load 加载 GGUF 模型。pooling 决定向量池化方式：
// nomic-embed-text 系列用 PoolLast；bge 系列用 PoolCLS；PoolUnspecified 由模型元数据决定。
func Load(path string, pooling Pooling) (*Model, error) {
	if err := loadBridge(); err != nil {
		return nil, err
	}
	cb := cString(path)
	h, _, _ := br.new.Call(uintptr(unsafe.Pointer(&cb[0])), uintptr(pooling))
	runtime.KeepAlive(cb)
	if h == 0 {
		return nil, fmt.Errorf("embed: 模型加载失败: %s", path)
	}
	dim, _, _ := br.dim.Call(h)
	if dim == 0 {
		errMsg := getError(h)
		br.free.Call(h)
		return nil, fmt.Errorf("embed: 模型无 embedding 输出（非 embedding 模型?）: %s", errMsg)
	}
	return &Model{handle: h, nEmbd: int(dim), pooling: pooling}, nil
}

// Close 释放模型与上下文（DLL 保持加载，便于再次 Load）。
func (m *Model) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.handle != 0 {
		br.free.Call(m.handle)
		m.handle = 0
	}
}

// Dim 返回向量维度。
func (m *Model) Dim() int { return m.nEmbd }

// Embed 将单条文本编码为向量（长度 = Dim()）。
func (m *Model) Embed(text string) ([]float32, error) {
	if text == "" {
		return make([]float32, m.nEmbd), nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.handle == 0 {
		return nil, fmt.Errorf("embed: 模型已关闭")
	}
	out := make([]float32, m.nEmbd)
	ct := cString(text)
	n, _, _ := br.enc.Call(m.handle, uintptr(unsafe.Pointer(&ct[0])), uintptr(unsafe.Pointer(&out[0])), uintptr(m.nEmbd))
	runtime.KeepAlive(ct)
	if n == 0 {
		return nil, fmt.Errorf("embed: 向量化失败: %s", getError(m.handle))
	}
	if int(n) < m.nEmbd {
		out = out[:int(n)]
	}
	return out, nil
}

// EmbedBatch 批量编码（复用同一上下文，逐条 encode）。
func (m *Model) EmbedBatch(texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, err := m.Embed(t)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// Cosine 计算两个向量的余弦相似度（-1 ~ 1；维度不一致返回 0）。
func Cosine(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}

// cString 返回 NUL 结尾的 UTF-8 字节切片（调用方在 syscall 调用期间 KeepAlive）。
func cString(s string) []byte {
	return append([]byte(s), 0)
}

// getError 从 cg_embed_error 读取错误信息到 Go 缓冲区（避免返回指针的 vet 误报）。
func getError(h uintptr) string {
	if br == nil || br.err == nil {
		return ""
	}
	buf := make([]byte, 256)
	n, _, _ := br.err.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	runtime.KeepAlive(buf)
	if n <= 0 {
		return ""
	}
	return string(buf[:n])
}
