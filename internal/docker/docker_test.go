package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OTA-EITA/depbot/internal/update"
)

func TestParseImage(t *testing.T) {
	cases := map[string]Image{
		"golang":                   {"docker.io", "library/golang"},
		"bitnami/redis":            {"docker.io", "bitnami/redis"},
		"ghcr.io/org/app":          {"ghcr.io", "org/app"},
		"localhost:5000/app":       {"localhost:5000", "app"},
		"registry.k8s.io/pause":    {"registry.k8s.io", "pause"},
		"gcr.io/distroless/static": {"gcr.io", "distroless/static"},
	}
	for in, want := range cases {
		if got := ParseImage(in); got != want {
			t.Errorf("ParseImage(%q) = %+v", in, got)
		}
	}
}

// registryStub requires a bearer token like Docker Hub and paginates tags.
func registryStub(t *testing.T) Registry {
	t.Helper()
	tags := map[string][]string{
		"library/golang": {"1.23", "1.24", "1.24.5", "1.25", "1.25.1", "1.26rc1", "1.24-alpine", "1.25-alpine", "1.25-alpine3.22", "latest", "alpine"},
		"library/alpine": {"3.19", "3.20", "3.21", "3.22", "edge"},
		"org/app":        {"v1.0.0", "v1.1.0", "1.2.0"},
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "t0k"})
			return
		}
		if r.Header.Get("Authorization") != "Bearer t0k" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="test",scope="repository:x:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		repo := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v2/"), "/tags/list")
		all, ok := tags[repo]
		if !ok {
			http.NotFound(w, r)
			return
		}
		// Two pages: first half, then the rest via a relative Link header.
		half := len(all) / 2
		page := all[:half]
		if r.URL.Query().Get("last") != "" {
			page = all[half:]
		} else {
			w.Header().Set("Link", `</v2/`+repo+`/tags/list?n=1000&last=x>; rel="next"`)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tags": page})
	}))
	t.Cleanup(srv.Close)
	return Registry{BaseURL: func(string) string { return srv.URL }}
}

func TestTagsFollowsTokenAndPages(t *testing.T) {
	tags, err := registryStub(t).Tags(context.Background(), ParseImage("alpine"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tags, ",") != "3.19,3.20,3.21,3.22,edge" {
		t.Fatalf("got %v", tags)
	}
	if _, err := registryStub(t).Tags(context.Background(), ParseImage("missing")); err == nil {
		t.Fatal("unknown repositories must fail")
	}
}

const dockerfile = `# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
FROM golang:1.24 AS tools
FROM build AS test
FROM alpine:3.19@sha256:0000000000000000000000000000000000000000000000000000000000000000
FROM ${BASE}
FROM scratch
FROM ghcr.io/org/app:v1.0.0
from alpine:3.19
FROM golang:latest
`

func TestFind(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "build"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "build", "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		t.Fatal(err)
	}
	ups, err := Find(context.Background(), root, registryStub(t))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, u := range ups {
		got = append(got, u.Name+":"+u.From+"->"+u.To+" "+u.Kind)
		if u.Dir != "build" {
			t.Errorf("dir %q", u.Dir)
		}
	}
	want := "golang:1.24-alpine->1.25-alpine minor|golang:1.24->1.25 minor|ghcr.io/org/app:v1.0.0->v1.1.0 minor|alpine:3.19->3.22 minor"
	if strings.Join(got, "|") != want {
		t.Fatalf("got  %s\nwant %s", strings.Join(got, "|"), want)
	}
	for _, u := range ups {
		if err := update.ApplyEdits(root, u.Edits); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(root, "build", "Dockerfile"))
	for _, want := range []string{
		"FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build",
		"FROM build AS test",
		"FROM alpine:3.19@sha256:",
		"from alpine:3.22",
		"FROM golang:latest",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %q in\n%s", want, data)
		}
	}
}

func TestIsDockerfile(t *testing.T) {
	for name, want := range map[string]bool{"Dockerfile": true, "Dockerfile.dev": true, "api.Dockerfile": true, "Containerfile": true, "dockerfile.md": false, "main.go": false} {
		if IsDockerfile(name) != want {
			t.Errorf("IsDockerfile(%q)", name)
		}
	}
}
