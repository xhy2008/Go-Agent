package codegraph

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// DB 封装 codegraph 的 SQLite 持久化：节点/边/文件指纹 + FTS5 全文索引 + 语义向量。
// 存储文件为 <root>/.go-agent/codegraph.db（与官方 codegraph 的 .codegraph 目录区分，避免 schema 冲突）。
type DB struct {
	db *sql.DB
}

// schema 建表语句（幂等）。nodes_fts 为 contentless FTS5 表，rowid 与 nodes.id 对齐。
const schema = `
CREATE TABLE IF NOT EXISTS nodes (
	id       INTEGER PRIMARY KEY,
	file     TEXT NOT NULL,
	line     INTEGER NOT NULL,
	kind     INTEGER NOT NULL,
	name     TEXT NOT NULL,
	receiver TEXT NOT NULL DEFAULT '',
	signature TEXT NOT NULL DEFAULT '',
	doc      TEXT NOT NULL DEFAULT '',
	embeds   TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS edges (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	from_id INTEGER NOT NULL,
	to_id   INTEGER NOT NULL,
	kind    INTEGER NOT NULL,
	site    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_edges_from ON edges(from_id);
CREATE INDEX IF NOT EXISTS idx_edges_to   ON edges(to_id);
CREATE TABLE IF NOT EXISTS files (
	path        TEXT PRIMARY KEY,
	fingerprint TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS vecs (
	node_id INTEGER PRIMARY KEY,
	dim     INTEGER NOT NULL,
	vec     BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE VIRTUAL TABLE IF NOT EXISTS nodes_fts USING fts5(
	name, receiver, signature, doc, file
);
`

// OpenDB 打开（或创建）项目根的 codegraph 数据库。
func OpenDB(root string) (*DB, error) {
	dir := filepath.Join(root, IndexDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", filepath.Join(dir, IndexFileName))
	if err != nil {
		return nil, fmt.Errorf("codegraph: 打开数据库失败: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite 单写者：串行化，避免锁竞争
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("codegraph: 初始化 schema 失败: %w", err)
	}
	return &DB{db: db}, nil
}

// Close 关闭数据库。
func (d *DB) Close() error { return d.db.Close() }

// Save 全量覆盖写入索引（节点/边/文件指纹/FTS/向量）。事务内原子提交。
func (d *DB) Save(ix *Index) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, t := range []string{"nodes_fts", "nodes", "edges", "files", "vecs", "meta"} {
		if _, err := tx.Exec("DELETE FROM " + t); err != nil {
			return fmt.Errorf("codegraph: 清空 %s 失败: %w", t, err)
		}
	}
	if _, err := tx.Exec(
		"INSERT INTO meta(key, value) VALUES('built_at', ?)",
		ix.BuiltAt.UTC().Format(time.RFC3339Nano),
	); err != nil {
		return err
	}

	stF, err := tx.Prepare("INSERT INTO files(path, fingerprint) VALUES(?, ?)")
	if err != nil {
		return err
	}
	defer stF.Close()
	for p, fp := range ix.FileFp {
		if _, err := stF.Exec(p, fp); err != nil {
			return err
		}
	}

	stN, err := tx.Prepare("INSERT INTO nodes(id, file, line, kind, name, receiver, signature, doc, embeds) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stN.Close()
	stFTS, err := tx.Prepare("INSERT INTO nodes_fts(rowid, name, receiver, signature, doc, file) VALUES(?, ?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stFTS.Close()
	for i := range ix.Nodes {
		n := &ix.Nodes[i]
		emb := strings.Join(n.Embeds, ",")
		if _, err := stN.Exec(n.ID, n.File, n.Line, int(n.Kind), n.Name, n.Receiver, n.Signature, n.Doc, emb); err != nil {
			return err
		}
		if _, err := stFTS.Exec(n.ID, n.Name, n.Receiver, n.Signature, n.Doc, n.File); err != nil {
			return err
		}
	}

	stE, err := tx.Prepare("INSERT INTO edges(from_id, to_id, kind, site) VALUES(?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stE.Close()
	for i := range ix.Edges {
		e := &ix.Edges[i]
		if _, err := stE.Exec(e.From, e.To, int(e.Kind), e.Site); err != nil {
			return err
		}
	}

	stV, err := tx.Prepare("INSERT INTO vecs(node_id, dim, vec) VALUES(?, ?, ?)")
	if err != nil {
		return err
	}
	defer stV.Close()
	for id, v := range ix.Vecs {
		blob := make([]byte, len(v)*4)
		for i, f := range v {
			binary.LittleEndian.PutUint32(blob[i*4:], math.Float32bits(f))
		}
		if _, err := stV.Exec(id, ix.VecDim, blob); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Load 从数据库读取完整索引（节点/边/文件指纹/向量/构建时间）。
func (d *DB) Load() (*Index, error) {
	ix := NewIndex()
	rows, err := d.db.Query("SELECT id, file, line, kind, name, receiver, signature, doc, embeds FROM nodes")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n Node
		var kind int
		var emb string
		if err := rows.Scan(&n.ID, &n.File, &n.Line, &kind, &n.Name, &n.Receiver, &n.Signature, &n.Doc, &emb); err != nil {
			return nil, err
		}
		n.Kind = Kind(kind)
		if emb != "" {
			n.Embeds = strings.Split(emb, ",")
		}
		ix.Nodes = append(ix.Nodes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range ix.Nodes {
		n := &ix.Nodes[i]
		ix.ByFile[n.File] = append(ix.ByFile[n.File], n.ID)
		ix.ByName[n.Name] = append(ix.ByName[n.Name], n.ID)
	}

	erows, err := d.db.Query("SELECT from_id, to_id, kind, site FROM edges")
	if err != nil {
		return nil, err
	}
	defer erows.Close()
	for erows.Next() {
		var e Edge
		var kind int
		if err := erows.Scan(&e.From, &e.To, &kind, &e.Site); err != nil {
			return nil, err
		}
		e.Kind = EdgeKind(kind)
		ix.Edges = append(ix.Edges, e)
	}
	if err := erows.Err(); err != nil {
		return nil, err
	}

	frows, err := d.db.Query("SELECT path, fingerprint FROM files")
	if err != nil {
		return nil, err
	}
	defer frows.Close()
	for frows.Next() {
		var p, fp string
		if err := frows.Scan(&p, &fp); err != nil {
			return nil, err
		}
		ix.FileFp[p] = fp
	}
	if err := frows.Err(); err != nil {
		return nil, err
	}

	vrows, err := d.db.Query("SELECT node_id, dim, vec FROM vecs")
	if err != nil {
		return nil, err
	}
	defer vrows.Close()
	for vrows.Next() {
		var id, dim int
		var blob []byte
		if err := vrows.Scan(&id, &dim, &blob); err != nil {
			return nil, err
		}
		vec := make([]float32, len(blob)/4)
		for i := range vec {
			vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
		}
		ix.VecDim = dim
		ix.Vecs[id] = vec
	}
	if err := vrows.Err(); err != nil {
		return nil, err
	}

	var builtAt string
	if err := d.db.QueryRow("SELECT value FROM meta WHERE key='built_at'").Scan(&builtAt); err == nil {
		if t, err := time.Parse(time.RFC3339Nano, builtAt); err == nil {
			ix.BuiltAt = t.Local()
		}
	}
	return ix, nil
}

// Search 通过 FTS5 全文检索，返回按相关度排序的节点 ID。
// query 拆分为词后以隐式 AND 组合（FTS5 默认语义）；空词返回 nil。
func (d *DB) Search(query string, limit int) ([]int, error) {
	q := ftsQuery(query)
	if q == "" {
		return nil, nil
	}
	rows, err := d.db.Query("SELECT rowid FROM nodes_fts WHERE nodes_fts MATCH ? ORDER BY rank LIMIT ?", q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ftsQuery 将查询串转为 FTS5 查询表达式：仅保留字母/数字/下划线，空格分隔（隐式 AND）。
func ftsQuery(q string) string {
	var words []string
	for _, w := range strings.FieldsFunc(q, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r >= 0x80)
	}) {
		if w != "" {
			words = append(words, strings.ToLower(w))
		}
	}
	return strings.Join(words, " ")
}
