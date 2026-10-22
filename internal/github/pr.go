package github

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// File is a file to commit. Mode is a git mode such as 100644.
type File struct {
	Path    string
	Content []byte
	Mode    string
}

// PullRequest is the subset of a pull request that is used.
type PullRequest struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
}

// BranchSHA returns the commit a branch points to.
func (c *Client) BranchSHA(ctx context.Context, owner, repo, branch string) (string, error) {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	_, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/git/ref/heads/%s", owner, repo, escapeRef(branch)), nil, &ref)
	return ref.Object.SHA, err
}

// OpenPR returns an open pull request from head, or nil.
func (c *Client) OpenPR(ctx context.Context, owner, repo, head string) (*PullRequest, error) {
	var prs []PullRequest
	q := url.Values{"state": {"open"}, "head": {owner + ":" + head}}
	if _, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls?%s", owner, repo, q.Encode()), nil, &prs); err != nil {
		return nil, err
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return &prs[0], nil
}

// CommitAndOpenPR commits files on a new branch created from base and opens a
// pull request. It uses the Git Data API, so it works without a clone.
func (c *Client) CommitAndOpenPR(ctx context.Context, owner, repo, base, branch, title, body string, files []File) (*PullRequest, error) {
	baseSHA, err := c.BranchSHA(ctx, owner, repo, base)
	if err != nil {
		return nil, fmt.Errorf("base branch: %w", err)
	}
	var baseCommit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if _, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/git/commits/%s", owner, repo, baseSHA), nil, &baseCommit); err != nil {
		return nil, err
	}

	type treeEntry struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
		Type string `json:"type"`
		SHA  string `json:"sha"`
	}
	var entries []treeEntry
	for _, f := range files {
		var blob struct {
			SHA string `json:"sha"`
		}
		in := map[string]string{"content": base64.StdEncoding.EncodeToString(f.Content), "encoding": "base64"}
		if _, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/git/blobs", owner, repo), in, &blob); err != nil {
			return nil, err
		}
		mode := f.Mode
		if mode == "" {
			mode = "100644"
		}
		entries = append(entries, treeEntry{Path: f.Path, Mode: mode, Type: "blob", SHA: blob.SHA})
	}
	var tree struct {
		SHA string `json:"sha"`
	}
	if _, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/git/trees", owner, repo),
		map[string]any{"base_tree": baseCommit.Tree.SHA, "tree": entries}, &tree); err != nil {
		return nil, err
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	if _, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/git/commits", owner, repo),
		map[string]any{"message": title, "tree": tree.SHA, "parents": []string{baseSHA}}, &commit); err != nil {
		return nil, err
	}
	ref := map[string]string{"ref": "refs/heads/" + branch, "sha": commit.SHA}
	if _, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/git/refs", owner, repo), ref, nil); err != nil {
		if !strings.Contains(err.Error(), "HTTP 422") {
			return nil, err
		}
		// The branch exists from an earlier run without an open PR: move it.
		force := map[string]any{"sha": commit.SHA, "force": true}
		if _, err := c.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/%s/git/refs/heads/%s", owner, repo, escapeRef(branch)), force, nil); err != nil {
			return nil, err
		}
	}
	var pr PullRequest
	in := map[string]string{"title": title, "head": branch, "base": base, "body": body}
	if _, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/pulls", owner, repo), in, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// escapeRef escapes each segment of a branch name for use in a URL path.
func escapeRef(ref string) string {
	parts := strings.Split(ref, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// IsNotFound reports a 404.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
