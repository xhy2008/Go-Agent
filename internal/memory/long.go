package memory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

var bucket = []byte("entries")

// Entry 是长期记忆中的一条文本块。
type Entry struct {
	ID     uint64         `json:"id"`
	Text   string         `json:"text"`
	Time   time.Time      `json:"time"`
	Tokens map[string]int `json:"tokens"`
}

// LongTerm 基于 bbolt 的长期记忆，使用关键词词频检索。
type LongTerm struct {
	db *bolt.DB
	// nextID 自增序列
	nextID uint64
}

var wordRe = regexp.MustCompile(`[a-zA-Z0-9_]{2,}|[\p{Han}]`)

// 中文停用字（低频信息词）
var stopChars = "的了我在你这是有和就都而及与或一个不很"

// OpenLongTerm 打开（或创建）长期记忆数据库。
func OpenLongTerm(path string) (*LongTerm, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, err
	}
	m := &LongTerm{db: db}
	err = db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(bucket)
		if err != nil {
			return err
		}
		c := b.Cursor()
		k, _ := c.Last()
		if k != nil {
			if id, err := strconv.ParseUint(string(k), 10, 64); err == nil {
				m.nextID = id
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return m, nil
}

// Close 关闭数据库。
func (m *LongTerm) Close() error { return m.db.Close() }

// Add 保存一条文本块（自动提取关键词）。
func (m *LongTerm) Add(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	m.nextID++
	entry := Entry{
		ID:     m.nextID,
		Text:   text,
		Time:   time.Now(),
		Tokens: tokenize(text),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	key := []byte(strconv.FormatUint(entry.ID, 10))
	return m.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucket).Put(key, data)
	})
}

// Search 按查询关键词检索最相关的 k 条记忆文本。
func (m *LongTerm) Search(ctx context.Context, query string, k int) ([]string, error) {
	q := tokenize(query)
	if len(q) == 0 {
		return nil, nil
	}
	type scored struct {
		text  string
		score float64
	}
	var results []scored

	err := m.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucket).Cursor()
		for key, val := c.First(); key != nil; key, val = c.Next() {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var e Entry
			if err := json.Unmarshal(val, &e); err != nil {
				continue
			}
			score := scoreTokens(q, e.Tokens)
			if score > 0 {
				results = append(results, scored{text: e.Text, score: score})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(results, func(i, j int) bool { return results[i].score > results[j].score })
	out := make([]string, 0, k)
	for i, r := range results {
		if i >= k {
			break
		}
		out = append(out, r.text)
	}
	return out, nil
}

// tokenize 分词：英文/数字连续词 + 中文单字（过滤停用字）。
func tokenize(text string) map[string]int {
	tokens := map[string]int{}
	for _, w := range wordRe.FindAllString(strings.ToLower(text), -1) {
		if len(w) == 1 {
			if strings.ContainsRune(stopChars, rune(w[0])) {
				continue
			}
		}
		tokens[w]++
	}
	return tokens
}

// scoreTokens 计算查询词与条目词频的交集加权得分。
func scoreTokens(q, e map[string]int) float64 {
	var score float64
	for w, qf := range q {
		if ef, ok := e[w]; ok {
			if qf < ef {
				score += float64(qf)
			} else {
				score += float64(ef)
			}
		}
	}
	return score
}

// Count 返回记忆条目总数（便于调试/状态栏）。
func (m *LongTerm) Count() int {
	var n int
	_ = m.db.View(func(tx *bolt.Tx) error {
		n = tx.Bucket(bucket).Stats().KeyN
		return nil
	})
	return n
}
