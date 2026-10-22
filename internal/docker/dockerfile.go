package docker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/OTA-EITA/depbot/internal/update"
	"github.com/OTA-EITA/depbot/internal/version"
)

// TagLister lists image tags; Registry implements it.
type TagLister interface {
	Tags(ctx context.Context, img Image) ([]string, error)
}

var fromRE = regexp.MustCompile(`(?i)^\s*FROM\s+(?:--platform=\S+\s+)?(\S+)(?:\s+AS\s+(\S+))?`)

// tagRE splits a tag into an optional "v", the numeric version and a suffix:
// "1.25-alpine" -> "", "1.25", "-alpine".
var tagRE = regexp.MustCompile(`^(v?)(\d+(?:\.\d+)*)(.*)$`)

// IsDockerfile reports the file names that are scanned.
func IsDockerfile(name string) bool {
	return name == "Dockerfile" || name == "Containerfile" ||
		strings.HasPrefix(name, "Dockerfile.") || strings.HasSuffix(name, ".Dockerfile")
}

// Find scans Dockerfiles below root and proposes newer tags that keep the
// same shape (precision and suffix), e.g. golang:1.24-alpine -> golang:1.25-alpine.
func Find(ctx context.Context, root string, reg TagLister) ([]update.Update, error) {
	var out []update.Update
	cache := map[Image][]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == "vendor" || d.Name() == "testdata" || (strings.HasPrefix(d.Name(), ".") && path != root)) {
			return filepath.SkipDir
		}
		if d.IsDir() || !IsDockerfile(d.Name()) {
			return nil
		}
		ups, err := findInFile(ctx, root, path, reg, cache)
		if err != nil {
			return err
		}
		out = append(out, ups...)
		return nil
	})
	return out, err
}

func findInFile(ctx context.Context, root, path string, reg TagLister, cache map[Image][]string) ([]update.Update, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- Dockerfiles found by walking the repository
	if err != nil {
		return nil, err
	}
	rel, _ := filepath.Rel(root, path)
	rel = filepath.ToSlash(rel)
	stages := map[string]bool{}
	var out []update.Update
	for i, line := range strings.Split(string(data), "\n") {
		m := fromRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ref, stage := m[1], m[2]
		skip := stages[strings.ToLower(ref)] || strings.Contains(ref, "$") || strings.Contains(ref, "@") || ref == "scratch"
		if stage != "" {
			stages[strings.ToLower(stage)] = true
		}
		if skip {
			continue // earlier build stage, build arg, digest-pinned or empty image
		}
		name, tag, found := cutTag(ref)
		if !found {
			continue
		}
		cur := tagRE.FindStringSubmatch(tag)
		if cur == nil {
			continue // e.g. "latest" or "bookworm"
		}
		img := ParseImage(name)
		tags, ok := cache[img]
		if !ok {
			if tags, err = reg.Tags(ctx, img); err != nil {
				return nil, fmt.Errorf("%s:%d %s: %w", rel, i+1, name, err)
			}
			cache[img] = tags
		}
		var candidates []string
		for _, t := range tags {
			if c := tagRE.FindStringSubmatch(t); c != nil && c[1] == cur[1] && c[3] == cur[3] {
				candidates = append(candidates, c[2])
			}
		}
		latest := version.Latest(cur[2], candidates, false)
		if latest == "" {
			continue
		}
		newTag := cur[1] + latest + cur[3]
		out = append(out, update.Update{
			Ecosystem: update.Docker, Name: name, Dir: filepath.ToSlash(filepath.Dir(rel)),
			From: tag, To: newTag, Kind: version.Kind(cur[2], latest),
			Edits: []update.Edit{{File: rel, Line: i + 1, Old: ref, New: name + ":" + newTag}},
		})
	}
	return out, nil
}

// cutTag splits "registry:5000/app:1.2" into "registry:5000/app" and "1.2".
func cutTag(ref string) (string, string, bool) {
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon < 0 || colon < slash {
		return ref, "", false
	}
	return ref[:colon], ref[colon+1:], true
}
