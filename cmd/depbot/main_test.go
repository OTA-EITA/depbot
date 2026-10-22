package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OTA-EITA/depbot/internal/docker"
	"github.com/OTA-EITA/depbot/internal/github"
	"github.com/OTA-EITA/depbot/internal/runner"
)

type tagsStub struct{}

func (tagsStub) Tags(_ context.Context, owner, repo string, _ int) ([]github.Tag, error) {
	return []github.Tag{{Name: "v5", SHA: strings.Repeat("a", 40)}, {Name: "v4", SHA: strings.Repeat("b", 40)}}, nil
}

type registryStub struct{}

func (registryStub) Tags(_ context.Context, _ docker.Image) ([]string, error) {
	return []string{"3.19", "3.22"}, nil
}

type prStub struct{ opened int }

func (p *prStub) OpenPR(context.Context, string, string, string) (*github.PullRequest, error) {
	return nil, nil
}

func (p *prStub) CommitAndOpenPR(context.Context, string, string, string, string, string, string, []github.File) (*github.PullRequest, error) {
	p.opened++
	return &github.PullRequest{HTMLURL: "https://example/pull/1"}, nil
}

func project(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o750); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		".github/workflows/ci.yml": "jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v4\n",
		"Dockerfile":               "FROM alpine:3.19\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func testDeps(prs *prStub) deps {
	return deps{sources: runner.Sources{GitHub: tagsStub{}, Registry: registryStub{}}, prs: prs}
}

const noGo = "--ecosystem=github-actions,docker"

func TestCheck(t *testing.T) {
	root := project(t)
	var out bytes.Buffer
	if code := run([]string{"check", noGo, root}, &out, io.Discard, testDeps(nil)); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"actions/checkout  v4", "alpine", "Dockerfile:1", ".github/workflows/ci.yml:4"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q:\n%s", want, out.String())
		}
	}
	if code := run([]string{"check", noGo, "--exit-code", root}, io.Discard, io.Discard, testDeps(nil)); code != exitFail {
		t.Errorf("--exit-code with updates must exit 1, got %d", code)
	}
	out.Reset()
	run([]string{"check", noGo, "--output", "json", root}, &out, io.Discard, testDeps(nil))
	var ups []map[string]any
	if err := json.Unmarshal(out.Bytes(), &ups); err != nil || len(ups) != 2 {
		t.Fatalf("json: %v %s", err, out.String())
	}
	out.Reset()
	run([]string{"check", noGo, "--ignore", "alpine", "--ignore", "actions/*", "--output", "markdown", root}, &out, io.Discard, testDeps(nil))
	if !strings.Contains(out.String(), "0 update(s)") {
		t.Errorf("ignored: %s", out.String())
	}
}

func TestUpdateAndPin(t *testing.T) {
	root := project(t)
	if code := run([]string{"update", noGo, "--pin", root}, io.Discard, io.Discard, testDeps(nil)); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	wf, _ := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	df, _ := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if !strings.Contains(string(wf), "actions/checkout@"+strings.Repeat("a", 40)+" # v5") || string(df) != "FROM alpine:3.22\n" {
		t.Fatalf("workflow:\n%s\nDockerfile:\n%s", wf, df)
	}
	var out bytes.Buffer
	run([]string{"check", noGo, root}, &out, io.Discard, testDeps(nil))
	if !strings.Contains(out.String(), "up to date") {
		t.Errorf("after update: %s", out.String())
	}
}

func TestPR(t *testing.T) {
	root := project(t)
	prs := &prStub{}
	var out bytes.Buffer
	if code := run([]string{"pr", noGo, "--repo", "o/r", root}, &out, io.Discard, testDeps(prs)); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	if prs.opened != 2 || strings.Count(out.String(), "open  ") != 2 {
		t.Fatalf("opened %d:\n%s", prs.opened, out.String())
	}
	t.Setenv("GITHUB_REPOSITORY", "")
	if code := run([]string{"pr", noGo, root}, io.Discard, io.Discard, testDeps(prs)); code != exitUsage {
		t.Errorf("missing --repo must be a usage error, got %d", code)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"bogus"}, {"check", "--ecosystem", "npm"}, {"check", "a", "b"}, {"check", "--output", "xml", "--ecosystem", "docker", t.TempDir()}} {
		if code := run(args, io.Discard, io.Discard, testDeps(nil)); code != exitUsage {
			t.Errorf("run(%v) = %d", args, code)
		}
	}
	var out bytes.Buffer
	if run([]string{"version"}, &out, io.Discard, testDeps(nil)) != exitOK || !strings.Contains(out.String(), "depbot") {
		t.Error("version")
	}
}

func TestGoProxy(t *testing.T) {
	t.Setenv("GOPROXY", "direct")
	if goProxy() != "https://proxy.golang.org" {
		t.Error("direct falls back to proxy.golang.org")
	}
	t.Setenv("GOPROXY", "https://goproxy.example,direct")
	if goProxy() != "https://goproxy.example" {
		t.Error("first HTTP proxy is used")
	}
}

func TestPinOnly(t *testing.T) {
	root := project(t)
	if code := run([]string{"update", "--ecosystem", "github-actions", "--pin-only", root}, io.Discard, io.Discard, testDeps(nil)); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	wf, _ := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if !strings.Contains(string(wf), "actions/checkout@"+strings.Repeat("b", 40)+" # v4") {
		t.Fatalf("workflow:\n%s", wf)
	}
}
