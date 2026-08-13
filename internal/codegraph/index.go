package codegraph

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	IndexDirName  = ".codegraph"
	IndexFileName = "codegraph.db"
	lockFileName  = "index.lock"
)

// Store 持有最近构建的索引与词法文件图缓存，支持进程内增量重建与并发查询。
type Store struct {
	mu     sync.RWMutex
	index  *Index
	graphs map[string]*FileGraph // 最近解析的文件图缓存（词法，增量复用）
	root   string                // 最近一次 Reindex 的项目根
	db     *DB                   // SQLite 持久化（nil = 尚未落盘）

	// VecBuilder 为可选的全量符号向量化回调（nil = 不启用语义检索）。
	// 每次 Reindex 构建索引后调用：入参全部符号，返回 node ID → 向量 与维度。
	// 由 internal/semantic 通过 VecBuilder 方法提供，codegraph 保持不依赖模型运行时。
	VecBuilder func(nodes []Node) (map[int][]float32, int, error)
}

// NewStore 创建空 Store。
func NewStore() *Store {
	return &Store{graphs: make(map[string]*FileGraph)}
}

// LoadStore 从磁盘加载已有索引（不存在则返回空 Store）。
func LoadStore(root string) *Store {
	s := NewStore()
	db, err := OpenDB(root)
	if err != nil {
		return s
	}
	ix, err := db.Load()
	if err != nil {
		db.Close()
		return s
	}
	s.db = db
	s.index = ix
	return s
}

// DB 返回当前数据库句柄（供 FTS5 检索；未落盘时可能为 nil）。
func (s *Store) DB() *DB {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.db
}

// Close 释放 SQLite 句柄（Windows 上释放文件锁）。
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		s.db.Close()
		s.db = nil
	}
}

// Index 返回当前索引（未构建时为 nil）。
func (s *Store) Index() *Index {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.index
}

// Reindex 增量重建 root 的索引：未变更文件复用缓存图，仅重解析变更/新增文件。
// 结果原子落盘（跨进程以 index.lock 非阻塞互斥）。
func (s *Store) Reindex(root string) (*Index, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.root = root

	relFiles, err := goFiles(root)
	if err != nil {
		return nil, err
	}
	fp := make(map[string]string, len(relFiles))
	for _, rel := range relFiles {
		fp[rel] = fingerprint(filepath.Join(root, filepath.FromSlash(rel)))
	}

	old := s.index
	if old == nil {
		return s.rebuildAll(root, fp, relFiles)
	}

	changed := false
	changedDirs := make(map[string]bool)
	for rel, f := range fp {
		if old.FileFp[rel] != f {
			changed = true
			changedDirs[dirOf(rel)] = true
		}
	}
	for rel := range old.FileFp {
		if _, ok := fp[rel]; !ok {
			changed = true // 文件被删除
			changedDirs[dirOf(rel)] = true
		}
	}
	if !changed {
		return old, nil // 快路径：无变更
	}

	keep := make(map[string]bool, len(relFiles))
	fgs := make([]*FileGraph, 0, len(relFiles))
	for _, rel := range relFiles {
		keep[rel] = true
		if g, ok := s.graphs[rel]; ok && old.FileFp[rel] == fp[rel] {
			fgs = append(fgs, g) // 未变更：复用缓存
			continue
		}
		g, err := ParseFile(filepath.Join(root, filepath.FromSlash(rel)), rel)
		if err != nil {
			continue // 解析失败的文件跳过（如生成中），不进入缓存
		}
		s.graphs[rel] = g
		fgs = append(fgs, g)
	}
	for rel := range s.graphs {
		if !keep[rel] {
			delete(s.graphs, rel)
		}
	}
	return s.finish(root, fp, fgs)
}

// rebuildAll 无旧索引时全量构建（预热文件图缓存）。
func (s *Store) rebuildAll(root string, fp map[string]string, relFiles []string) (*Index, error) {
	s.graphs = make(map[string]*FileGraph, len(relFiles))
	fgs := make([]*FileGraph, 0, len(relFiles))
	for _, rel := range relFiles {
		g, err := ParseFile(filepath.Join(root, filepath.FromSlash(rel)), rel)
		if err != nil {
			continue
		}
		s.graphs[rel] = g
		fgs = append(fgs, g)
	}
	return s.finish(root, fp, fgs)
}

func (s *Store) finish(root string, fp map[string]string, fgs []*FileGraph) (*Index, error) {
	modulePath := moduleOf(root)
	ix, err := Build(root, modulePath, fgs)
	if err != nil {
		return nil, err
	}
	// 语义向量化（可选）：在落盘前填充 Vecs，随索引持久化
	if s.VecBuilder != nil {
		if vecs, dim, verr := s.VecBuilder(ix.Nodes); verr == nil {
			ix.Vecs = vecs
			ix.VecDim = dim
		}
	}
	valid := make(map[string]string, len(fgs))
	for _, g := range fgs {
		valid[g.Path] = fp[g.Path]
	}
	ix.FileFp = valid
	ix.BuiltAt = time.Now().Round(0)

	// 跨进程互斥写盘：锁被占用说明另一进程正在重建，跳过写盘（内存索引不受影响）
	db := s.db
	if db == nil {
		var derr error
		db, derr = OpenDB(root)
		if derr != nil {
			return ix, derr
		}
		s.db = db
	}
	lockPath := filepath.Join(root, IndexDirName, lockFileName)
	lf, lerr := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if lerr == nil {
		lerr = db.Save(ix)
		lf.Close()
		os.Remove(lockPath)
	}

	s.index = ix
	return ix, lerr
}

// goFiles 收集项目内全部支持的源文件（相对路径，正斜杠）。
// 忽略规则对齐官方 codegraph：默认忽略清单 + 根/嵌套 .gitignore 过滤，
// 目录被忽略时整棵子树跳过；.git 与索引数据目录（.codegraph 及 .codegraph-*）
// 始终跳过；>1MB 文件跳过（生成的 bundle/压缩产物）。
func goFiles(root string) ([]string, error) {
	base := scopedIgnore{dir: root, ig: buildDefaultIgnore(root)}
	var out []string
	collectFiles(root, root, []scopedIgnore{base}, &out)
	sort.Strings(out)
	return out, nil
}

// collectFiles 递归收集 dir 下受支持的源文件。matchers 为从根到当前目录的
// .gitignore 匹配器链（相对各自声明目录求值）。
func collectFiles(root, dir string, matchers []scopedIgnore, out *[]string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // 不可读目录跳过，不中断扫描
	}
	// 本目录的 .gitignore 适用于其下所有内容；根目录的已并入基础匹配器，不重复加载。
	active := matchers
	if dir != root {
		if own := loadIgnore(dir); own != nil {
			active = append(matchers, *own)
		}
	}
	for _, e := range entries {
		name := e.Name()
		// 不进入 git 内部与 CodeGraph 数据目录（含 .codegraph-* 变体，对齐原版）。
		if name == ".git" || name == IndexDirName || strings.HasPrefix(name, IndexDirName+"-") {
			continue
		}
		full := filepath.Join(dir, name)
		if e.IsDir() {
			if isIgnored(full, true, active) {
				continue
			}
			collectFiles(root, full, active, out)
			continue
		}
		if !e.Type().IsRegular() {
			continue // 符号链接等不跟随（差异：原版跟随 in-root symlink 并 realpath 去重）
		}
		if isIgnored(full, false, active) {
			continue
		}
		if !sourceExts[strings.ToLower(filepath.Ext(name))] {
			continue
		}
		if info, ierr := e.Info(); ierr == nil && info.Size() > maxFileSize {
			continue // >1MB 跳过（对齐原版 MAX_FILE_SIZE）
		}
		rel, _ := filepath.Rel(root, full)
		*out = append(*out, filepath.ToSlash(rel))
	}
}

// fingerprint 计算文件指纹：内容 sha256 + mtime + size。
func fingerprint(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) + ":" + itoa(int(fi.ModTime().UnixNano())) + ":" + itoa(int(fi.Size()))
}

// moduleOf 读取 go.mod 的 module 名（用于 import 路径 → 目录映射）。
func moduleOf(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}
