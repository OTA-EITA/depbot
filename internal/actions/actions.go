// Package actions finds newer versions of GitHub Actions referenced with
// `uses:` in workflows and composite actions, and can pin them to commit SHAs.
package actions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/OTA-EITA/depbot/internal/github"
	"github.com/OTA-EITA/depbot/internal/update"
	"github.com/OTA-EITA/depbot/internal/version"
)

// TagLister lists tags of a repository; *github.Client implements it.
type TagLister interface {
	Tags(ctx context.Context, owner, repo string, maxPages int) ([]github.Tag, error)
}

// Options control the scan.
type Options struct {
	// Pin converts tag references (uses: x/y@v4) to commit SHAs with the
	// version kept in a comment, as recommended by GitHub's hardening guide.
	Pin bool
	// PinOnly pins references at their current version without bumping them.
	PinOnly bool
}

// usesRE matches `uses: owner/repo[/path]@ref # comment`.
var usesRE = regexp.MustCompile(`uses:\s*["']?([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)((?:/[^@\s"']*)?)@([^\s"'#]+)(["']?)(\s*#\s*(\S+))?`)

var shaRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

type reference struct {
	file, owner, repo, path, ref, comment, matched string
	// quote is the closing quote of a quoted value, kept before any comment.
	quote string
	line  int
}

// Find scans .github/workflows and action.yml files below root.
func Find(ctx context.Context, root string, gh TagLister, opt Options) ([]update.Update, error) {
	refs, err := scan(root)
	if err != nil {
		return nil, err
	}
	cache := map[string][]github.Tag{}
	var out []update.Update
	for _, r := range refs {
		key := r.owner + "/" + r.repo
		tags, ok := cache[key]
		if !ok {
			tags, err = gh.Tags(ctx, r.owner, r.repo, 3)
			if err != nil {
				return nil, fmt.Errorf("%s:%d %s: %w", r.file, r.line, key, err)
			}
			cache[key] = tags
		}
		if u, ok := propose(r, tags, opt); ok {
			out = append(out, u)
		}
	}
	return out, nil
}

func propose(r reference, tags []github.Tag, opt Options) (update.Update, bool) {
	names := make([]string, len(tags))
	shaOf := map[string]string{}
	for i, t := range tags {
		names[i] = t.Name
		shaOf[t.Name] = t.SHA
	}
	action := r.owner + "/" + r.repo + r.path
	u := update.Update{Ecosystem: update.Actions, Name: r.owner + "/" + r.repo, Dir: "."}
	edit := func(newRef, comment string) {
		repl := action + "@" + newRef + r.quote
		if comment != "" {
			repl += " # " + comment
		}
		u.Edits = []update.Edit{{File: r.file, Line: r.line, Old: r.matched, New: repl}}
	}

	switch {
	case shaRE.MatchString(r.ref):
		// Pinned: the comment carries the version, e.g. "# v4.2.2".
		if opt.PinOnly || version.Canonical(r.comment) == "" {
			return u, false
		}
		latest := version.Latest(r.comment, names, false)
		if latest == "" || shaOf[latest] == "" {
			return u, false
		}
		u.From, u.To, u.Kind = r.comment, latest, version.Kind(r.comment, latest)
		edit(shaOf[latest], latest)
	case version.Canonical(r.ref) != "":
		latest := version.Latest(r.ref, names, false)
		if opt.PinOnly {
			latest = ""
		}
		switch {
		case latest != "" && opt.Pin && shaOf[latest] != "":
			u.From, u.To, u.Kind = r.ref, latest, version.Kind(r.ref, latest)
			edit(shaOf[latest], latest)
		case latest != "":
			u.From, u.To, u.Kind = r.ref, latest, version.Kind(r.ref, latest)
			edit(latest, "")
		case (opt.Pin || opt.PinOnly) && shaOf[r.ref] != "":
			u.From, u.To, u.Kind = r.ref, r.ref, "pin"
			edit(shaOf[r.ref], r.ref)
		default:
			return u, false
		}
	default:
		return u, false // branch names such as main are left alone
	}
	return u, true
}

func scan(root string) ([]reference, error) {
	var files []string
	wf := filepath.Join(root, ".github", "workflows")
	if entries, err := os.ReadDir(wf); err == nil {
		for _, e := range entries {
			if !e.IsDir() && (strings.HasSuffix(e.Name(), ".yml") || strings.HasSuffix(e.Name(), ".yaml")) {
				files = append(files, filepath.Join(wf, e.Name()))
			}
		}
	}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == "vendor" || d.Name() == "testdata" || (d.Name() == ".git" && path != root)) {
			return filepath.SkipDir
		}
		if !d.IsDir() && (d.Name() == "action.yml" || d.Name() == "action.yaml") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	var refs []reference
	for _, f := range files {
		data, err := os.ReadFile(f) // #nosec G304 -- files found by scanning the repository
		if err != nil {
			return nil, err
		}
		rel, _ := filepath.Rel(root, f)
		for i, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			m := usesRE.FindStringSubmatchIndex(line)
			if m == nil {
				continue
			}
			sub := func(n int) string {
				if m[2*n] < 0 {
					return ""
				}
				return line[m[2*n]:m[2*n+1]]
			}
			start, end := m[2], m[11] // through the closing quote
			if m[12] >= 0 {
				end = m[13] // through the version comment
			}
			refs = append(refs, reference{
				file: filepath.ToSlash(rel), line: i + 1,
				owner: sub(1), repo: sub(2), path: sub(3), ref: sub(4), quote: sub(5), comment: sub(7),
				matched: line[start:end],
			})
		}
	}
	return refs, nil
}
