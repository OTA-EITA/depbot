package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyEdits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(path, []byte("FROM golang:1.24 AS build\nFROM alpine:3.19\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	err := ApplyEdits(dir, []Edit{
		{File: "Dockerfile", Line: 1, Old: "golang:1.24", New: "golang:1.25"},
		{File: "Dockerfile", Line: 2, Old: "alpine:3.19", New: "alpine:3.22"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "FROM golang:1.25 AS build\nFROM alpine:3.22\n" {
		t.Fatalf("got %q", got)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
		t.Errorf("mode not preserved: %v", info.Mode())
	}
	if err := ApplyEdits(dir, []Edit{{File: "Dockerfile", Line: 1, Old: "golang:1.24", New: "x"}}); err == nil {
		t.Fatal("a stale edit must be refused")
	}
}

func TestTitleAndBranch(t *testing.T) {
	u := Update{Ecosystem: Actions, Name: "actions/checkout", Dir: ".", From: "v4", To: "v5", Kind: "major"}
	if u.Title() != "chore(deps): bump actions/checkout from v4 to v5" || u.Branch() != "depbot/github-actions/actions-checkout-v5" {
		t.Fatalf("%q %q", u.Title(), u.Branch())
	}
	g := Update{Ecosystem: GoMod, Name: "golang.org/x/text", Dir: "tools", To: "v0.30.0"}
	if g.Branch() != "depbot/gomod/tools/golang.org-x-text-v0.30.0" {
		t.Fatalf("%q", g.Branch())
	}
	p := Update{Name: "actions/setup-go", To: "v6", Kind: "pin"}
	if p.Title() != "chore(deps): pin actions/setup-go to a commit SHA (v6)" {
		t.Fatalf("%q", p.Title())
	}
}
