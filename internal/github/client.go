// Package github is a small REST client for the endpoints depbot needs:
// listing tags, and creating commits, branches and pull requests through the
// Git Data API (so no local git checkout is required to open a PR).
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to api.github.com or a GitHub Enterprise Server API.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// ErrNotFound is returned for HTTP 404.
var ErrNotFound = errors.New("not found")

// New returns a client for api.github.com.
func New(token string) *Client {
	return &Client{BaseURL: "https://api.github.com", Token: token}
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) (http.Header, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	url := path
	if !strings.HasPrefix(path, "http") {
		url = strings.TrimSuffix(c.BaseURL, "/") + path
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return resp.Header, fmt.Errorf("%s %s: %w", method, path, ErrNotFound)
	}
	if resp.StatusCode >= 300 {
		return resp.Header, fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.Header, fmt.Errorf("%s %s: %w", method, path, err)
		}
	}
	return resp.Header, nil
}

// Tag is a git tag and the commit it points to.
type Tag struct {
	Name string
	SHA  string
}

// Tags lists up to maxPages*100 tags of a repository, newest first as returned by the API.
func (c *Client) Tags(ctx context.Context, owner, repo string, maxPages int) ([]Tag, error) {
	var out []Tag
	next := fmt.Sprintf("/repos/%s/%s/tags?per_page=100", owner, repo)
	for page := 0; next != "" && page < maxPages; page++ {
		var raw []struct {
			Name   string `json:"name"`
			Commit struct {
				SHA string `json:"sha"`
			} `json:"commit"`
		}
		h, err := c.do(ctx, http.MethodGet, next, nil, &raw)
		if err != nil {
			return nil, err
		}
		for _, t := range raw {
			out = append(out, Tag{Name: t.Name, SHA: t.Commit.SHA})
		}
		next = nextLink(h.Get("Link"))
	}
	return out, nil
}

// nextLink extracts rel="next" from a Link header.
func nextLink(link string) string {
	for _, part := range strings.Split(link, ",") {
		segs := strings.Split(part, ";")
		if len(segs) < 2 || !strings.Contains(segs[1], `rel="next"`) {
			continue
		}
		return strings.Trim(strings.TrimSpace(segs[0]), "<>")
	}
	return ""
}
