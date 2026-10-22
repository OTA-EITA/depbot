// Package gomod finds newer versions of the direct requirements of Go modules
// using the module proxy protocol, and applies them with the go command.
package gomod

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/OTA-EITA/depbot/internal/update"
	"github.com/OTA-EITA/depbot/internal/version"
)

// Proxy queries a Go module proxy such as https://proxy.golang.org.
type Proxy struct {
	URL  string
	HTTP *http.Client
}

// Versions lists the published versions of a module.
func (p Proxy) Versions(ctx context.Context, path string) ([]string, error) {
	esc, err := module.EscapePath(path)
	if err != nil {
		return nil, err
	}
	body, err := p.get(ctx, strings.TrimSuffix(p.URL, "/")+"/"+esc+"/@v/list")
	if err != nil {
		return nil, err
	}
	return strings.Fields(body), nil
}

// LatestInfo returns the proxy's @latest version, used for modules that only
// publish pseudo-versions.
func (p Proxy) LatestInfo(ctx context.Context, path string) (string, error) {
	esc, err := module.EscapePath(path)
	if err != nil {
		return "", err
	}
	body, err := p.get(ctx, strings.TrimSuffix(p.URL, "/")+"/"+esc+"/@latest")
	if err != nil {
		return "", err
	}
	var info struct{ Version string }
	if err := json.Unmarshal([]byte(body), &info); err != nil {
		return "", err
	}
	return info.Version, nil
}

func (p Proxy) get(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	hc := p.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return string(data), nil
}

// Find scans go.mod files below root and proposes updates for direct
// requirements within the same major version (a new major version is a
// different module path in Go).
func Find(ctx context.Context, root string, proxy Proxy) ([]update.Update, error) {
	var out []update.Update
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "vendor" || d.Name() == "testdata" || (strings.HasPrefix(d.Name(), ".") && path != root)) {
			return filepath.SkipDir
		}
		if d.IsDir() || d.Name() != "go.mod" {
			return nil
		}
		ups, err := findInFile(ctx, root, path, proxy)
		if err != nil {
			return err
		}
		out = append(out, ups...)
		return nil
	})
	return out, err
}

func findInFile(ctx context.Context, root, path string, proxy Proxy) ([]update.Update, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- go.mod files found by walking the repository
	if err != nil {
		return nil, err
	}
	mf, err := modfile.ParseLax(path, data, nil)
	if err != nil {
		return nil, err
	}
	rel, _ := filepath.Rel(root, filepath.Dir(path))
	var out []update.Update
	for _, r := range mf.Require {
		if r.Indirect {
			continue
		}
		cur := r.Mod.Version
		next, err := newer(ctx, proxy, r.Mod.Path, cur)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", r.Mod.Path, err)
		}
		if next == "" {
			continue
		}
		modPath, dir := r.Mod.Path, rel
		out = append(out, update.Update{
			Ecosystem: update.GoMod, Name: modPath, Dir: filepath.ToSlash(dir),
			From: cur, To: next, Kind: version.Kind(cur, next),
			Apply: func(root string) error { return goGet(filepath.Join(root, dir), modPath, next) },
		})
	}
	return out, nil
}

func newer(ctx context.Context, proxy Proxy, path, cur string) (string, error) {
	vs, err := proxy.Versions(ctx, path)
	if err != nil {
		return "", err
	}
	if module.IsPseudoVersion(cur) || len(vs) == 0 {
		latest, err := proxy.LatestInfo(ctx, path)
		if err != nil {
			return "", err
		}
		if semver.Compare(latest, cur) > 0 && semver.Major(latest) == semver.Major(cur) {
			return latest, nil
		}
		return "", nil
	}
	// Module versions are always full semver, so precision is always 3.
	best := version.Latest(cur, vs, true)
	if version.IsPrerelease(cur) {
		for _, v := range vs {
			if semver.Major(v) == semver.Major(cur) && semver.Compare(v, cur) > 0 && (best == "" || semver.Compare(v, best) > 0) {
				best = v
			}
		}
	}
	return best, nil
}

// toolchain is the GOTOOLCHAIN used for go get. "auto" lets the go command
// fetch a newer toolchain when an update requires one (raising the go
// directive, as Dependabot does), even where CI sets GOTOOLCHAIN=local.
func toolchain() string {
	if t := os.Getenv("DEPBOT_GOTOOLCHAIN"); t != "" {
		return t
	}
	return "auto"
}

// goGet updates one requirement and tidies the module, which also refreshes go.sum.
func goGet(dir, path, ver string) error {
	for _, args := range [][]string{{"get", path + "@" + ver}, {"mod", "tidy"}} {
		cmd := exec.Command("go", args...) // #nosec G204 -- fixed binary, arguments from go.mod and the proxy
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOTOOLCHAIN="+toolchain())
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("go %s: %w\n%s", strings.Join(args, " "), err, out)
		}
	}
	return nil
}
