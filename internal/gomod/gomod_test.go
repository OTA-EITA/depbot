package gomod

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func proxyStub(t *testing.T) Proxy {
	t.Helper()
	responses := map[string]string{
		"/example.com/lib/@v/list":    "v1.0.0\nv1.2.0\nv1.3.0-rc.1\nv1.2.1\n",
		"/example.com/lib/v2/@v/list": "v2.0.0\nv2.1.0\n",
		"/example.com/fresh/@v/list":  "v0.9.0\n",
		"/example.com/pseudo/@v/list": "",
		"/example.com/pseudo/@latest": `{"Version":"v0.0.0-20260101000000-abcdefabcdef"}`,
		"/example.com/!upper/@v/list": "v1.0.0\nv1.0.1\n",
		"/example.com/pre/@v/list":    "v1.0.0-rc.1\nv1.0.0-rc.2\nv0.9.0\n",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := responses[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return Proxy{URL: srv.URL}
}

func TestFind(t *testing.T) {
	root := t.TempDir()
	gomod := `module example.com/app

go 1.25

require (
	example.com/lib v1.0.0
	example.com/lib/v2 v2.0.0
	example.com/fresh v0.9.0
	example.com/pseudo v0.0.0-20250101000000-000000000000
	example.com/Upper v1.0.0
	example.com/pre v1.0.0-rc.1
	example.com/indirect v1.0.0 // indirect
)
`
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(gomod), 0o600); err != nil {
		t.Fatal(err)
	}
	// Modules under testdata and vendor are not scanned.
	if err := os.MkdirAll(filepath.Join(root, "testdata", "x"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "testdata", "x", "go.mod"), []byte("module x\nrequire example.com/missing v1.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ups, err := Find(context.Background(), root, proxyStub(t))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, u := range ups {
		got[u.Name] = u.From + "->" + u.To + " " + u.Kind
		if u.Apply == nil || u.Dir != "." {
			t.Errorf("%s: missing Apply or wrong dir %q", u.Name, u.Dir)
		}
	}
	want := map[string]string{
		"example.com/lib":    "v1.0.0->v1.2.1 minor",
		"example.com/lib/v2": "v2.0.0->v2.1.0 minor",
		"example.com/pseudo": "v0.0.0-20250101000000-000000000000->v0.0.0-20260101000000-abcdefabcdef patch",
		"example.com/Upper":  "v1.0.0->v1.0.1 patch",
		"example.com/pre":    "v1.0.0-rc.1->v1.0.0-rc.2 patch",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}
}

func TestProxyErrors(t *testing.T) {
	if _, err := proxyStub(t).Versions(context.Background(), "example.com/unknown"); err == nil {
		t.Fatal("404 must be an error")
	}
	if _, err := (Proxy{URL: "http://x"}).Versions(context.Background(), "Invalid Path"); err == nil {
		t.Fatal("invalid module paths must be rejected")
	}
}
