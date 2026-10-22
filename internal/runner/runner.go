// Package runner ties the ecosystems together: it collects updates, groups
// the same version bump across files, applies them and opens pull requests.
package runner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/OTA-EITA/depbot/internal/actions"
	"github.com/OTA-EITA/depbot/internal/docker"
	"github.com/OTA-EITA/depbot/internal/github"
	"github.com/OTA-EITA/depbot/internal/gomod"
	"github.com/OTA-EITA/depbot/internal/update"
)

// Sources are the backends used to look up versions.
type Sources struct {
	Proxy    gomod.Proxy
	GitHub   actions.TagLister
	Registry docker.TagLister
}

// Options select what is collected.
type Options struct {
	Ecosystems map[string]bool
	Ignore     []string
	Pin        bool
}

// Collect runs the selected ecosystems below root.
func Collect(ctx context.Context, root string, src Sources, opt Options) ([]update.Update, error) {
	var all []update.Update
	if opt.Ecosystems[update.GoMod] {
		ups, err := gomod.Find(ctx, root, src.Proxy)
		if err != nil {
			return nil, fmt.Errorf("gomod: %w", err)
		}
		all = append(all, ups...)
	}
	if opt.Ecosystems[update.Actions] {
		ups, err := actions.Find(ctx, root, src.GitHub, actions.Options{Pin: opt.Pin})
		if err != nil {
			return nil, fmt.Errorf("github-actions: %w", err)
		}
		all = append(all, ups...)
	}
	if opt.Ecosystems[update.Docker] {
		ups, err := docker.Find(ctx, root, src.Registry)
		if err != nil {
			return nil, fmt.Errorf("docker: %w", err)
		}
		all = append(all, ups...)
	}
	var kept []update.Update
	for _, u := range all {
		if !ignored(u.Name, opt.Ignore) {
			kept = append(kept, u)
		}
	}
	return Group(kept), nil
}

func ignored(name string, patterns []string) bool {
	for _, p := range patterns {
		if wildcard(p, name) {
			return true
		}
	}
	return false
}

func wildcard(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for i, p := range parts[1:] {
		if i == len(parts)-2 {
			return strings.HasSuffix(s, p)
		}
		idx := strings.Index(s, p)
		if idx < 0 {
			return false
		}
		s = s[idx+len(p):]
	}
	return true
}

// Group merges text edits of the same dependency moving to the same version,
// so one pull request bumps actions/checkout in every workflow at once.
func Group(ups []update.Update) []update.Update {
	var out []update.Update
	index := map[string]int{}
	for _, u := range ups {
		if u.Apply != nil {
			out = append(out, u)
			continue
		}
		// From is not part of the key: v3 and v4 users of one action moving to
		// v5 share a branch, so they must share a pull request.
		key := u.Ecosystem + "\x00" + u.Name + "\x00" + u.To
		if i, ok := index[key]; ok {
			out[i].Edits = append(out[i].Edits, u.Edits...)
			if !strings.Contains(", "+out[i].From+", ", ", "+u.From+", ") {
				out[i].From += ", " + u.From
			}
			continue
		}
		index[key] = len(out)
		out = append(out, u)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Ecosystem != out[j].Ecosystem {
			return out[i].Ecosystem < out[j].Ecosystem
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Apply performs one update below root.
func Apply(root string, u update.Update) error {
	if u.Apply != nil {
		return u.Apply(root)
	}
	return update.ApplyEdits(root, u.Edits)
}

// touchedFiles lists the files an update may change, relative to root.
func touchedFiles(u update.Update) []string {
	if u.Apply != nil {
		return []string{filepath.Join(u.Dir, "go.mod"), filepath.Join(u.Dir, "go.sum")}
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range u.Edits {
		if !seen[e.File] {
			seen[e.File] = true
			out = append(out, e.File)
		}
	}
	return out
}

// PRClient is the subset of the GitHub client used to open pull requests.
type PRClient interface {
	OpenPR(ctx context.Context, owner, repo, head string) (*github.PullRequest, error)
	CommitAndOpenPR(ctx context.Context, owner, repo, base, branch, title, body string, files []github.File) (*github.PullRequest, error)
}

// OpenPRs opens one pull request per update. Each update is applied to a
// fresh copy of the repository, so updates never leak into each other.
func OpenPRs(ctx context.Context, root, owner, repo, base string, gh PRClient, ups []update.Update, dryRun bool, log io.Writer) error {
	for _, u := range ups {
		branch := u.Branch()
		if existing, err := gh.OpenPR(ctx, owner, repo, branch); err != nil {
			return err
		} else if existing != nil {
			fmt.Fprintf(log, "skip  %s: already open as %s\n", u.Title(), existing.HTMLURL)
			continue
		}
		files, err := changedFiles(root, u)
		if err != nil {
			return fmt.Errorf("%s: %w", u.Name, err)
		}
		if len(files) == 0 {
			fmt.Fprintf(log, "skip  %s: no file changed\n", u.Title())
			continue
		}
		if dryRun {
			var paths []string
			for _, f := range files {
				paths = append(paths, f.Path)
			}
			fmt.Fprintf(log, "would open %s on %s (%s)\n", u.Title(), branch, strings.Join(paths, ", "))
			continue
		}
		pr, err := gh.CommitAndOpenPR(ctx, owner, repo, base, branch, u.Title(), Body(u), files)
		if err != nil {
			return fmt.Errorf("%s: %w", u.Name, err)
		}
		fmt.Fprintf(log, "open  %s: %s\n", u.Title(), pr.HTMLURL)
	}
	return nil
}

// changedFiles applies u to a temporary copy of root and returns the files
// whose content differs from the original.
func changedFiles(root string, u update.Update) ([]github.File, error) {
	tmp, err := os.MkdirTemp("", "depbot-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := copyTree(root, tmp); err != nil {
		return nil, err
	}
	if err := Apply(tmp, u); err != nil {
		return nil, err
	}
	var out []github.File
	for _, rel := range touchedFiles(u) {
		after, err := os.ReadFile(filepath.Join(tmp, rel)) // #nosec G304 -- file inside the temporary copy
		if err != nil {
			continue // e.g. a module without dependencies has no go.sum
		}
		before, _ := os.ReadFile(filepath.Join(root, rel)) // #nosec G304 -- file inside the repository
		if bytes.Equal(before, after) {
			continue
		}
		mode := "100644"
		if info, err := os.Stat(filepath.Join(root, rel)); err == nil && info.Mode().Perm()&0o111 != 0 {
			mode = "100755"
		}
		out = append(out, github.File{Path: filepath.ToSlash(rel), Content: after, Mode: mode})
	}
	return out, nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") && rel != "." {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path) // #nosec G304 -- copying the repository being updated
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

// Body is the pull request description.
func Body(u update.Update) string {
	var b strings.Builder
	if u.Kind == "pin" {
		fmt.Fprintf(&b, "Pins **%s** to the commit SHA of `%s`, so a moved or compromised tag cannot change what runs in CI.\n\n", u.Name, u.To)
	} else {
		fmt.Fprintf(&b, "Bumps **%s** from `%s` to `%s` (%s update).\n\n", u.Name, u.From, u.To, u.Kind)
	}
	if link := releaseLink(u); link != "" {
		fmt.Fprintf(&b, "Release: %s\n\n", link)
	}
	b.WriteString("Changed files:\n")
	for _, f := range touchedFiles(u) {
		fmt.Fprintf(&b, "- `%s`\n", filepath.ToSlash(f))
	}
	b.WriteString("\n---\nOpened by [depbot](https://github.com/OTA-EITA/depbot).\n")
	return b.String()
}

func releaseLink(u update.Update) string {
	switch u.Ecosystem {
	case update.GoMod:
		return "https://pkg.go.dev/" + u.Name + "@" + u.To
	case update.Actions:
		return "https://github.com/" + u.Name + "/releases/tag/" + u.To
	case update.Docker:
		img := docker.ParseImage(u.Name)
		if name, ok := strings.CutPrefix(img.Repository, "library/"); ok && img.Registry == "docker.io" {
			return "https://hub.docker.com/_/" + name + "/tags"
		}
		if img.Registry == "docker.io" {
			return "https://hub.docker.com/r/" + img.Repository + "/tags"
		}
	}
	return ""
}
