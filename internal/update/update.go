// Package update defines a proposed dependency update and how text edits are
// applied to files.
package update

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Ecosystems.
const (
	GoMod   = "gomod"
	Actions = "github-actions"
	Docker  = "docker"
)

// Edit replaces Old with New on one line of a file (1-based).
type Edit struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

// Update is one dependency that can be moved to a newer version.
type Update struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	// Dir is the directory the update applies to, relative to the repository root.
	Dir   string `json:"dir"`
	From  string `json:"from"`
	To    string `json:"to"`
	Kind  string `json:"kind"`
	Edits []Edit `json:"edits,omitempty"`
	// Apply performs the update below root. Text-based updates use ApplyEdits.
	Apply func(root string) error `json:"-"`
}

// Title is the pull request and commit title.
func (u Update) Title() string {
	if u.Kind == "pin" {
		return fmt.Sprintf("chore(deps): pin %s to a commit SHA (%s)", u.Name, u.To)
	}
	return fmt.Sprintf("chore(deps): bump %s from %s to %s", u.Name, u.From, u.To)
}

// Branch is a stable branch name, so reruns find existing pull requests.
func (u Update) Branch() string {
	r := strings.NewReplacer("/", "-", ":", "-", "@", "-", " ", "-")
	dir := ""
	if u.Dir != "" && u.Dir != "." {
		dir = r.Replace(u.Dir) + "/"
	}
	return "depbot/" + u.Ecosystem + "/" + dir + r.Replace(u.Name) + "-" + r.Replace(u.To)
}

// ApplyEdits applies text edits below root. It refuses to edit a line that no
// longer contains the expected text, so a stale plan never corrupts a file.
func ApplyEdits(root string, edits []Edit) error {
	byFile := map[string][]Edit{}
	for _, e := range edits {
		byFile[e.File] = append(byFile[e.File], e)
	}
	for file, es := range byFile {
		path := filepath.Join(root, file)
		data, err := os.ReadFile(path) // #nosec G304 -- paths come from the repository scan
		if err != nil {
			return err
		}
		lines := strings.Split(string(data), "\n")
		for _, e := range es {
			if e.Line < 1 || e.Line > len(lines) || !strings.Contains(lines[e.Line-1], e.Old) {
				return fmt.Errorf("%s:%d: expected %q, file changed since it was scanned", file, e.Line, e.Old)
			}
			lines[e.Line-1] = strings.Replace(lines[e.Line-1], e.Old, e.New, 1)
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), info.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}
