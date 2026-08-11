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

// Brave 是 Brave Search API 后端。
// 文档: https://api.search.brave.com/app/documentation/web-search
type Brave struct {
	APIKey string
	Client *http.Client
}

func (b *Brave) Name() string { return "brave" }

func (b *Brave) http() *http.Client {
	if b.Client != nil {
		return b.Client
	}
	return http.DefaultClient
}

func (b *Brave) Search(ctx context.Context, query string, maxResults int) ([]Result, error) {
	reqURL := fmt.Sprintf("https://api.search.brave.com/res/v1/web/search?q=%s&count=%d",
		url.QueryEscape(query), maxResults)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Subscription-Token", b.APIKey)
	req.Header.Set("Accept", "application/json")

	resp, err := b.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("brave api error %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var payload struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, err
	}

	out := make([]Result, 0, len(payload.Web.Results))
	for _, r := range payload.Web.Results {
		out = append(out, Result{Title: r.Title, URL: r.URL, Snippet: r.Description})
	}
	return out, nil
}
