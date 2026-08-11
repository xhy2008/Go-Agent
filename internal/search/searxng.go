package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Searxng 是自托管 SearXNG 实例后端。
// 请求 <BaseURL>/search?q=...&format=json。
type Searxng struct {
	BaseURL string
	Client  *http.Client
}

func (s *Searxng) Name() string { return "searxng" }

func (s *Searxng) http() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return http.DefaultClient
}

func (s *Searxng) Search(ctx context.Context, query string, maxResults int) ([]Result, error) {
	base := strings.TrimRight(s.BaseURL, "/")
	reqURL := fmt.Sprintf("%s/search?q=%s&format=json&safesearch=0", base, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("searxng error %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var payload struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&payload); err != nil {
		return nil, err
	}

	out := make([]Result, 0, len(payload.Results))
	for _, r := range payload.Results {
		if r.URL == "" {
			continue
		}
		out = append(out, Result{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	if len(out) > maxResults {
		out = out[:maxResults]
	}
	return out, nil
}
