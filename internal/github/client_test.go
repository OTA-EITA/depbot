package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestTagsPaginates(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing token")
		}
		if r.URL.Query().Get("page") == "" {
			w.Header().Set("Link", `<`+srv.URL+r.URL.Path+`?per_page=100&page=2>; rel="next", <x>; rel="last"`)
			_, _ = w.Write([]byte(`[{"name":"v5","commit":{"sha":"aaa"}}]`))
			return
		}
		_, _ = w.Write([]byte(`[{"name":"v4","commit":{"sha":"bbb"}}]`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Token: "tok"}
	tags, err := c.Tags(context.Background(), "actions", "checkout", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[0].Name != "v5" || tags[1].SHA != "bbb" {
		t.Fatalf("got %+v", tags)
	}
	missing := httptest.NewServer(http.NotFoundHandler())
	defer missing.Close()
	if _, err := (&Client{BaseURL: missing.URL}).Tags(context.Background(), "a", "b", 1); !IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
}

// fakeGitHub records the Git Data API calls of CommitAndOpenPR.
type fakeGitHub struct {
	mu         sync.Mutex
	calls      []string
	blobs      []string
	refExists  bool
	treeBody   map[string]any
	commitBody map[string]any
}

func (f *fakeGitHub) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/git/ref/heads/main":
			_, _ = w.Write([]byte(`{"object":{"sha":"base"}}`))
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/git/commits/base":
			_, _ = w.Write([]byte(`{"tree":{"sha":"basetree"}}`))
		case r.Method == "POST" && r.URL.Path == "/repos/o/r/git/blobs":
			content, _ := base64.StdEncoding.DecodeString(in["content"].(string))
			f.blobs = append(f.blobs, string(content))
			_, _ = w.Write([]byte(`{"sha":"blob1"}`))
		case r.Method == "POST" && r.URL.Path == "/repos/o/r/git/trees":
			f.treeBody = in
			_, _ = w.Write([]byte(`{"sha":"tree1"}`))
		case r.Method == "POST" && r.URL.Path == "/repos/o/r/git/commits":
			f.commitBody = in
			_, _ = w.Write([]byte(`{"sha":"commit1"}`))
		case r.Method == "POST" && r.URL.Path == "/repos/o/r/git/refs":
			if f.refExists {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"message":"Reference already exists"}`))
				return
			}
			w.WriteHeader(http.StatusCreated)
		case r.Method == "PATCH" && strings.HasPrefix(r.URL.Path, "/repos/o/r/git/refs/heads/"):
			_, _ = w.Write([]byte(`{}`))
		case r.Method == "POST" && r.URL.Path == "/repos/o/r/pulls":
			_, _ = w.Write([]byte(`{"number":7,"html_url":"https://github.com/o/r/pull/7"}`))
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/pulls":
			if r.URL.Query().Get("head") == "o:depbot/open" {
				_, _ = w.Write([]byte(`[{"number":3,"html_url":"u"}]`))
				return
			}
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
}

func TestCommitAndOpenPR(t *testing.T) {
	f := &fakeGitHub{}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL}
	pr, err := c.CommitAndOpenPR(context.Background(), "o", "r", "main", "depbot/x", "chore(deps): bump x", "body",
		[]File{{Path: "go.mod", Content: []byte("module x\n")}})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 7 || f.blobs[0] != "module x\n" || f.treeBody["base_tree"] != "basetree" || f.commitBody["message"] != "chore(deps): bump x" {
		t.Fatalf("pr %+v blobs %v tree %v commit %v", pr, f.blobs, f.treeBody, f.commitBody)
	}

	// An existing branch without an open PR is force-moved to the new commit.
	f2 := &fakeGitHub{refExists: true}
	srv2 := httptest.NewServer(f2.handler(t))
	defer srv2.Close()
	if _, err := (&Client{BaseURL: srv2.URL}).CommitAndOpenPR(context.Background(), "o", "r", "main", "depbot/a/b", "t", "b", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(f2.calls, "\n"), "PATCH /repos/o/r/git/refs/heads/depbot/a/b") {
		t.Fatalf("calls: %v", f2.calls)
	}
}

func TestOpenPR(t *testing.T) {
	f := &fakeGitHub{}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL}
	pr, err := c.OpenPR(context.Background(), "o", "r", "depbot/open")
	if err != nil || pr == nil || pr.Number != 3 {
		t.Fatalf("got %+v %v", pr, err)
	}
	pr, err = c.OpenPR(context.Background(), "o", "r", "depbot/none")
	if err != nil || pr != nil {
		t.Fatalf("got %+v %v", pr, err)
	}
}

func TestNextLinkAndNotFound(t *testing.T) {
	if got := nextLink(`<https://a/b?page=2>; rel="next", <https://a/b?page=9>; rel="last"`); got != "https://a/b?page=2" {
		t.Fatalf("got %q", got)
	}
	if nextLink(`<https://a/b?page=9>; rel="last"`) != "" {
		t.Fatal("no next")
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	_, err := (&Client{BaseURL: srv.URL}).BranchSHA(context.Background(), "o", "r", "x")
	if !IsNotFound(err) {
		t.Fatalf("got %v", err)
	}
}
