package runner

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OTA-EITA/depbot/internal/github"
	"github.com/OTA-EITA/depbot/internal/update"
)

func TestGroupMergesSameBump(t *testing.T) {
	e := func(f string) []update.Edit { return []update.Edit{{File: f, Line: 1, Old: "a", New: "b"}} }
	got := Group([]update.Update{
		{Ecosystem: update.Actions, Name: "actions/checkout", From: "v4", To: "v5", Edits: e("a.yml")},
		{Ecosystem: update.Actions, Name: "actions/checkout", From: "v3", To: "v5", Edits: e("b.yml")},
		{Ecosystem: update.Actions, Name: "actions/checkout", From: "v4", To: "v5", Edits: e("c.yml")},
		{Ecosystem: update.Actions, Name: "actions/checkout", From: "v4.2.2", To: "v5.0.0", Edits: e("d.yml")},
		{Ecosystem: update.Docker, Name: "alpine", From: "3.19", To: "3.22", Edits: e("Dockerfile")},
		{Ecosystem: update.GoMod, Name: "x", To: "v1.1.0", Apply: func(string) error { return nil }},
	})
	if len(got) != 4 {
		t.Fatalf("got %d updates: %+v", len(got), got)
	}
	if got[0].Ecosystem != update.Docker || got[3].Ecosystem != update.GoMod {
		t.Errorf("not sorted by ecosystem: %+v", got)
	}
	co := got[1]
	if len(co.Edits) != 3 || co.From != "v4, v3" {
		t.Errorf("checkout v5: %+v", co)
	}
}

func TestIgnored(t *testing.T) {
	if !ignored("golang.org/x/text", []string{"golang.org/x/*"}) || ignored("alpine", []string{"golang"}) {
		t.Fatal("ignored")
	}
}

type fakePRs struct {
	open   map[string]bool
	opened []string
	files  map[string][]github.File
}

func (f *fakePRs) OpenPR(_ context.Context, _, _, head string) (*github.PullRequest, error) {
	if f.open[head] {
		return &github.PullRequest{HTMLURL: "https://example/pull/1"}, nil
	}
	return nil, nil
}

func (f *fakePRs) CommitAndOpenPR(_ context.Context, _, _, base, branch, title, body string, files []github.File) (*github.PullRequest, error) {
	f.opened = append(f.opened, branch)
	if f.files == nil {
		f.files = map[string][]github.File{}
	}
	f.files[branch] = files
	return &github.PullRequest{HTMLURL: "https://example/pull/" + branch}, nil
}

func TestOpenPRs(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM alpine:3.19\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("image=alpine:3.19\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	ups := []update.Update{
		{Ecosystem: update.Docker, Name: "alpine", Dir: ".", From: "3.19", To: "3.22", Kind: "minor",
			Edits: []update.Edit{{File: "Dockerfile", Line: 1, Old: "alpine:3.19", New: "alpine:3.22"}, {File: "run.sh", Line: 1, Old: "alpine:3.19", New: "alpine:3.22"}}},
		{Ecosystem: update.Actions, Name: "actions/checkout", Dir: ".", From: "v4", To: "v5", Kind: "major"},
		{Ecosystem: update.Docker, Name: "busybox", Dir: ".", From: "1", To: "2", Kind: "major"},
	}
	prs := &fakePRs{open: map[string]bool{"depbot/github-actions/actions-checkout-v5": true}}
	var log bytes.Buffer
	if err := OpenPRs(context.Background(), root, "o", "r", "main", prs, ups, false, &log); err != nil {
		t.Fatal(err)
	}
	if len(prs.opened) != 1 || prs.opened[0] != "depbot/docker/alpine-3.22" {
		t.Fatalf("opened %v\n%s", prs.opened, log.String())
	}
	files := prs.files["depbot/docker/alpine-3.22"]
	if len(files) != 2 || string(files[0].Content) != "FROM alpine:3.22\n" || files[1].Mode != "100755" {
		t.Fatalf("files %+v", files)
	}
	for _, want := range []string{"skip  chore(deps): bump actions/checkout from v4 to v5: already open", "skip  chore(deps): bump busybox from 1 to 2: no file changed"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log missing %q:\n%s", want, log.String())
		}
	}
	// The working tree itself is never modified.
	if data, _ := os.ReadFile(filepath.Join(root, "Dockerfile")); string(data) != "FROM alpine:3.19\n" {
		t.Fatalf("working tree changed: %q", data)
	}

	log.Reset()
	prs2 := &fakePRs{}
	if err := OpenPRs(context.Background(), root, "o", "r", "main", prs2, ups[:1], true, &log); err != nil {
		t.Fatal(err)
	}
	if len(prs2.opened) != 0 || !strings.Contains(log.String(), "would open chore(deps): bump alpine from 3.19 to 3.22 on depbot/docker/alpine-3.22 (Dockerfile, run.sh)") {
		t.Fatalf("dry run: %v\n%s", prs2.opened, log.String())
	}
}

func TestBody(t *testing.T) {
	b := Body(update.Update{Ecosystem: update.Docker, Name: "golang", From: "1.24", To: "1.25", Kind: "minor", Edits: []update.Edit{{File: "Dockerfile"}}})
	if !strings.Contains(b, "https://hub.docker.com/_/golang/tags") || !strings.Contains(b, "`Dockerfile`") {
		t.Errorf("%s", b)
	}
	p := Body(update.Update{Ecosystem: update.Actions, Name: "actions/checkout", To: "v5", Kind: "pin"})
	if !strings.Contains(p, "Pins **actions/checkout**") || !strings.Contains(p, "releases/tag/v5") {
		t.Errorf("%s", p)
	}
	g := Body(update.Update{Ecosystem: update.GoMod, Name: "golang.org/x/text", Dir: "tools", To: "v0.30.0", Apply: func(string) error { return nil }})
	if !strings.Contains(g, "pkg.go.dev/golang.org/x/text@v0.30.0") || !strings.Contains(g, "`tools/go.sum`") {
		t.Errorf("%s", g)
	}
}
